package lexdoc

import (
	"regexp"
	"strings"
	"time"
)

// When an amending act's changes take effect.
//
// The date a law's change list gives an amending act is the day it was signed, and many acts take
// effect later, in parts: "Loven træder i kraft den 1. januar 2009, jf. dog stk. 2-4. Stk. 2. § 1,
// nr. 13-15, træder i kraft den 1. juli 2008." Applied on the day it was signed, the 2006 police
// reform was in the retsplejelov half a year before the consolidation that does not print it, and
// a change of a later act drafted against the text before it met words it had not yet replaced.
// The act says when, in words regular enough to read; what it does not say plainly stays on the
// day it was signed.

// InForce is an act's entry into force: a date for the act as a whole, and dates for the parts it
// names.
type InForce struct {
	Default time.Time
	Rules   []InForceRule
}

// InForceRule is one part of an act taking effect on its own date: its provision Section, and the
// changes Numbers of it; all of them when Numbers is empty.
type InForceRule struct {
	Section string
	Numbers [][2]int
	Date    time.Time
}

// Deferred is the day of a part an act leaves to a minister to set in force, "Ministeren fastsætter
// tidspunktet for ikrafttræden af § 2", until an order sets it: no day this text can be read on.
var Deferred = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// When is the day the change numbered number in the act's provision section takes effect, or
// false when the act does not say. A day an order sets goes before the act's deferring it.
func (f InForce) When(section, number string) (time.Time, bool) {
	n := Atoi(strings.TrimRight(strings.TrimSpace(number), "."))
	var named, whole *InForceRule
	better := func(have, r *InForceRule) bool {
		return have == nil || have.Date.Equal(Deferred) && !r.Date.Equal(Deferred)
	}
	for i := range f.Rules {
		r := &f.Rules[i]
		if r.Section != section || section == "" {
			continue
		}
		if len(r.Numbers) == 0 {
			if better(whole, r) {
				whole = r
			}
			continue
		}
		for _, span := range r.Numbers {
			if n >= span[0] && n <= span[1] && better(named, r) {
				named = r
			}
		}
	}
	switch {
	case named != nil && !(named.Date.Equal(Deferred) && whole != nil && !whole.Date.Equal(Deferred)):
		return named.Date, true
	case whole != nil:
		return whole.Date, true
	case named != nil:
		return named.Date, true
	}
	return f.Default, !f.Default.IsZero()
}

// reLawExcept is the act as a whole but for parts named: "loven med undtagelse af § 12".
var reLawExcept = regexp.MustCompile(`^loven\s+med\s+undtagelse\s+af\s+§`)

var (
	reInForceVerb = regexp.MustCompile(`(?i)\btræder\s+i\s+kraft\s+`)
	reInForceDate = regexp.MustCompile(`(?i)^den\s+([0-9]{1,2})\.\s*(januar|februar|marts|april|maj|juni|juli|august|september|oktober|november|december)\s+([0-9]{4})`)
	reDayAfter    = regexp.MustCompile(`(?i)^(?:fra\s+)?(?:dagen\s+efter|ved)\s+(?:lovens\s+|bekendtgørelsens\s+)?(?:bekendtgørelse(?:n)?|offentliggørelse(?:n)?)\b`)
	reWholeAct    = regexp.MustCompile(`(?i)^(?:loven|denne\s+lov|bekendtgørelsen|denne\s+bekendtgørelse)\s*$`)
	// A sentence starts at a capital or "§" after a full stop: "… jf. dog stk. 2-4. Stk. 2. § 1,
	// nr. 13-15, træder i kraft …".
	reSentenceStart = regexp.MustCompile(`\.\s+(?:Stk\.\s*[0-9]+\.\s*)?([A-ZÆØÅ§])`)
	reInForceRef    = regexp.MustCompile(`§(§?)\s*([0-9]+(?:\s*[-–]\s*[0-9]+)?(?:\s*(?:,|og)\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?)*)((?:\s*,\s*nr\.\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?(?:\s*(?:,|og)\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?)*)?)`)
	reOwnMarker     = regexp.MustCompile(`^§\s*[0-9]+(?:\s*[a-zæøå])?\.?\s+(?:Stk\.\s*[0-9]+\.\s*)?([A-ZÆØÅ])`)
	reActsOwn       = regexp.MustCompile(`(?i)^(?:lovens|denne\s+lovs|bekendtgørelsens)\s+`)
	reTrailingNote  = regexp.MustCompile(`\s+[0-9]{1,2}\)$`)
	reNumberSpan    = regexp.MustCompile(`([0-9]+)(?:\s*[-–]\s*([0-9]+))?`)
	inForceMonths   = map[string]time.Month{"januar": 1, "februar": 2, "marts": 3, "april": 4, "maj": 5, "juni": 6,
		"juli": 7, "august": 8, "september": 9, "oktober": 10, "november": 11, "december": 12}
)

// ParseInForce reads an act's entry-into-force provisions. signed is the day the act was signed,
// which "dagen efter bekendtgørelsen i Lovtidende" counts from.
func ParseInForce(texts []string, signed time.Time) InForce {
	return parseInForce(texts, signed, "")
}

// ParseInForceOrder reads an order that sets an act in force, "Bekendtgørelse om ikrafttræden af
// …": "I medfør af § 7, stk. 4, i lov nr. 462 af 9. juni 2004 … bestemmes, at lovens § 1, nr. 21
// træder i kraft den 1. juni 2005", or "Lov nr. 462 af 9. juni 2004 … træder i kraft den …". act
// is how the order cites the act, "nr. 462 af 9. juni 2004"; a sentence naming another act is not
// read.
func ParseInForceOrder(texts []string, act string) InForce {
	return parseInForce(texts, time.Time{}, act)
}

func parseInForce(texts []string, signed time.Time, act string) InForce {
	var f InForce
	for _, text := range texts {
		text = Squeeze(text)
		if act == "" {
			for _, m := range reDeferral.FindAllStringSubmatch(text, -1) {
				// "Økonomi- og erhvervsministeren fastsætter tidspunktet for ikrafttræden af § 2."
				// Only the act's own provisions: "§ 11 f … i lov om afgift af elektricitet" names
				// the law it amends.
				subject := strings.TrimSpace(m[1])
				if strings.Contains(subject, " i lov") || strings.Contains(subject, "stk.") || strings.Contains(subject, "bilag") {
					continue
				}
				f.Rules = append(f.Rules, inForceRules(subject, Deferred)...)
			}
			for _, m := range reDeferredChanges.FindAllStringSubmatch(text, -1) {
				// "Skatteministeren fastsætter ikrafttrædelsestidspunktet for … ændringerne i
				// vægtafgiftslovens § 4 som affattet ved denne lovs § 2, nr. 1": the changes named
				// by the provisions of the law they bring.
				for _, r := range reOwnChangeRef.FindAllStringSubmatch(m[1], -1) {
					f.Rules = append(f.Rules, inForceRules("§ "+r[1], Deferred)...)
				}
			}
		}
		for _, loc := range reInForceVerb.FindAllStringIndex(text, -1) {
			date, ok := inForceDate(text[loc[1]:], signed)
			if !ok {
				continue
			}
			subject := strings.TrimSpace(text[sentenceStart(text, loc[0]):loc[0]])
			if act != "" {
				// "… bestemmes, at lovens § 1, nr. 21 1) træder i kraft": the order's subject
				// follows its "at", and a note's marker is no part of it.
				if k := strings.LastIndex(subject, " at "); k >= 0 {
					subject = strings.TrimSpace(subject[k+len(" at "):])
				}
				subject = strings.TrimSpace(reTrailingNote.ReplaceAllString(subject, ""))
				low := strings.ToLower(subject)
				switch {
				case strings.Contains(low, "nr. ") && strings.Contains(low, " af ") && !strings.HasPrefix(low, "lovens") && !strings.HasPrefix(low, "§"):
					// "Lov nr. 462 af 9. juni 2004 om …": the act as a whole, if it is this one.
					if strings.Contains(low, strings.ToLower(act)) && !strings.Contains(subject, "§") && f.Default.IsZero() {
						f.Default = date
					}
					continue
				case (low == "loven" || reLawExcept.MatchString(low)) && f.Default.IsZero():
					// "… at loven med undtagelse af § 12 træder i kraft den 8. september 2009": the
					// act's date; the parts excepted have dates of their own.
					f.Default = date
					continue
				}
			}
			// "§ 2 Loven træder i kraft …": the provision's own marker printed before its text.
			subject = reOwnMarker.ReplaceAllString(subject, "$1")
			subject = strings.TrimSpace(reActsOwn.ReplaceAllString(subject, ""))
			if reWholeAct.MatchString(subject) {
				if f.Default.IsZero() {
					f.Default = date
				}
				continue
			}
			if ms := reOwnChangeRef.FindAllStringSubmatch(subject, -1); ms != nil {
				// "§ 52 a, stk. 1, i lov om vandforsyning m.v. som affattet ved denne lovs § 3,
				// nr. 1, … træder i kraft": the amended law's provision, named by the change of
				// this act that brings it.
				for _, m := range ms {
					f.Rules = append(f.Rules, inForceRules("§ "+m[1], date)...)
				}
				// "Lovens § 3, nr. 2 og 3, og § 5, nr. 3-5, og §§ 3 b og 3 c i lov om …": what
				// comes before the first provision of the other law is the act's own.
				if k := reOtherLaw.FindStringIndex(subject); k != nil {
					if j := strings.LastIndex(subject[:k[0]], "§"); j > 0 {
						own := strings.TrimRight(strings.TrimSpace(subject[:j]), ",§ ")
						own = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(own, "og")), ",")
						if strings.Contains(own, "§") && !strings.Contains(own, "stk.") {
							f.Rules = append(f.Rules, inForceRules(strings.TrimSpace(reActsOwn.ReplaceAllString(own, "")), date)...)
						}
					}
				}
				continue
			}
			if strings.Contains(subject, "stk.") || !strings.HasPrefix(subject, "§") {
				continue // a subsection of the act's own provision, or a subject this does not read
			}
			f.Rules = append(f.Rules, inForceRules(subject, date)...)
		}
	}
	return f
}

// reOwnChangeRef is a change of this act named by the provision of the law it brings: "som
// affattet ved denne lovs § 3, nr. 1", "som ændret ved lovens § 4, nr. 3 og 5".
var reOwnChangeRef = regexp.MustCompile(`(?i)\b(?:affattet|ændret|indsat|ophævet)\s+ved\s+(?:denne\s+lovs|lovens)\s+§\s*([0-9]+\s*,\s*nr\.\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?(?:\s*(?:,|og)\s*[0-9]+(?:\s*[-–]\s*[0-9]+)?)*)`)

// reOtherLaw is where a subject turns to another law's provisions: "§§ 3 b og 3 c i lov om …".
var reOtherLaw = regexp.MustCompile(`(?i)\bi\s+lov(?:en)?\s+om\b`)

// reDeferral is a minister left to set parts of the act in force, up to the end of the sentence.
var reDeferral = regexp.MustCompile(`(?i)\bfastsætter\s+tidspunktet\s+for\s+ikrafttræd(?:en|enen|else|elsen)\s+af\s+((?:lovens\s+)?§[^.]*(?:\.\s*[0-9][^.]*)*)`)

// reDeferredChanges is a minister left to set in force changes the act names by what they bring,
// up to the end of the sentence.
var reDeferredChanges = regexp.MustCompile(`(?i)\bfastsætter\s+(?:ikrafttrædelsestidspunktet|tidspunktet\s+for\s+ikrafttræd(?:en|enen|else|elsen))\s+(?:for|af)\s+([^.]*(?:\.\s*[0-9][^.]*)*)`)

// RulesCover reports whether the rules name the change numbered number of the act's provision
// section.
func RulesCover(rules []InForceRule, section, number string) bool {
	n := Atoi(strings.TrimRight(strings.TrimSpace(number), "."))
	for _, r := range rules {
		if r.Section != section {
			continue
		}
		if len(r.Numbers) == 0 {
			return true
		}
		for _, span := range r.Numbers {
			if n >= span[0] && n <= span[1] {
				return true
			}
		}
	}
	return false
}

// InForceRulesOf are the provisions and changes a reference names, "§ 1, nr. 2, 8-13 og 26", as
// rules without a date.
func InForceRulesOf(ref string) []InForceRule { return inForceRules(ref, time.Time{}) }

// inForceRules are the rules a subject naming the act's provisions and changes gives a date.
func inForceRules(subject string, date time.Time) []InForceRule {
	var rules []InForceRule
	subject = strings.TrimSpace(reActsOwn.ReplaceAllString(subject, ""))
	for _, m := range reInForceRef.FindAllStringSubmatch(subject, -1) {
		var sections []string
		for _, sp := range reNumberSpan.FindAllStringSubmatch(m[2], -1) {
			from, to := Atoi(sp[1]), Atoi(sp[1])
			if sp[2] != "" {
				to = Atoi(sp[2])
			}
			for k := from; k <= to && k-from < 200; k++ {
				sections = append(sections, itoa(k))
			}
		}
		var numbers [][2]int
		if m[1] == "" && m[3] != "" {
			for _, sp := range reNumberSpan.FindAllStringSubmatch(m[3], -1) {
				from, to := Atoi(sp[1]), Atoi(sp[1])
				if sp[2] != "" {
					to = Atoi(sp[2])
				}
				numbers = append(numbers, [2]int{from, to})
			}
		}
		for _, sec := range sections {
			rules = append(rules, InForceRule{Section: sec, Numbers: numbers, Date: date})
		}
	}
	return rules
}

// inForceDate reads the date after "træder i kraft".
func inForceDate(after string, signed time.Time) (time.Time, bool) {
	if m := reInForceDate.FindStringSubmatch(after); m != nil {
		return time.Date(Atoi(m[3]), inForceMonths[strings.ToLower(m[2])], Atoi(m[1]), 0, 0, 0, 0, time.UTC), true
	}
	if reDayAfter.MatchString(after) && !signed.IsZero() {
		return signed.AddDate(0, 0, 1), true
	}
	return time.Time{}, false
}

// sentenceStart is where the sentence holding position at begins.
func sentenceStart(text string, at int) int {
	start := 0
	for _, m := range reSentenceStart.FindAllStringSubmatchIndex(text[:at], -1) {
		start = m[2]
	}
	return start
}
