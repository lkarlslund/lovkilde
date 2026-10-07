package search

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/artifactdir"
)

// Ranking judged against questions people ask. Each question carries the document a lawyer
// would open, as a pattern over its title; the score is mean reciprocal rank and hit@10 over
// distinct places (document, address), with no filters and with "love og bekendtgørelser,
// gældende". The first set tuned DefaultRanking (TestRankingSweep); the second took no part in
// tuning and is how overfitting would show.
//
// Gated on RETSIN_RANK_JUDGE because it reads the real index (the current artifact's, or
// RETSIN_INDEX): an ordinary `go test ./...` must not depend on it. It runs in seconds.

type judgment struct {
	q    string
	want string // regexp over the expected document's title
	any  bool   // the answer need not be a law: an order or guidance counts
}

var judgments = []judgment{
	{"forældremyndighed", `forældreansvarsloven`, false},
	{"samvær med barnet", `forældreansvarsloven`, false},
	{"barselsorlov", `barselsloven`, false},
	{"opsigelse af lejemål", `^lov om leje$`, false},
	{"huslejenævn", `^lov om leje$|lov om boligforhold|boligregulering`, false},
	{"skilsmisse", `ægteskabs indgåelse og opløsning|ægteskabsloven`, false},
	{"ægtefællebidrag", `ægteskabs indgåelse og opløsning|ægteskabsloven`, false},
	{"tvangsarv", `arveloven`, false},
	{"testamente", `arveloven`, false},
	{"feriepenge", `ferieloven|lov om ferie\b`, false},
	{"opsigelsesvarsel funktionær", `funktionær`, false},
	{"sygedagpenge", `sygedagpenge`, false},
	{"førtidspension", `social pension|almindelig førtidspension`, false},
	{"folkepension", `social pension`, false},
	{"kontanthjælp", `aktiv socialpolitik`, false},
	{"behandling af personoplysninger", `databeskyttelsesloven|databeskyttelse`, false},
	{"aktindsigt", `af forvaltningsloven|offentlighed i forvaltningen|offentlighedsloven`, false},
	{"partshøring", `af forvaltningsloven`, false},
	{"spirituskørsel", `færdselsloven`, false},
	{"hastighedsgrænse", `færdselsloven`, false},
	{"nødværge", `straffeloven`, false},
	{"tyveri straf", `straffeloven`, false},
	{"byggetilladelse", `byggeloven|bygningsreglement`, false},
	{"lokalplan", `planloven|lov om planlægning`, false},
	{"miljøgodkendelse", `miljøbeskyttelse`, false},
	{"momsfradrag", `momsloven`, false},
	{"personfradrag", `\(personskatteloven\)`, false},
	{"ejendomsværdiskat", `ejendomsværdiskat|ejendomsskat`, false},
	{"mangler ved købt vare", `lov om køb$`, false},
	{"fortrydelsesret", `af lov om forbrugeraftaler$`, false},
	{"ansættelsesbevis", `ansættelsesbeviser`, false},
	{"ligeløn", `lige løn|ligeløn`, false},
	{"indfødsret", `lov om dansk indfødsret`, false},
	{"familiesammenføring", `udlændingeloven`, false},
	{"undervisningspligt", `lov om folkeskolen$`, false},
	{"uddannelsesstøtte", `su-loven`, false},
	{"hund i snor", `hundeloven|lov om hunde`, false},
	{"dyrevelfærd", `dyrevelfærd`, false},
	{"våbentilladelse", `våbenloven|lov om våben`, false},
	{"jagttegn", `jagt`, false},
	{"ophavsret", `af lov om ophavsret$`, false},
	{"varemærke", `varemærke`, false},
	{"anpartsselskab kapital", `selskabsloven|aktie- og anpartsselskaber`, false},
	{"konkursbo", `konkursloven`, false},
	{"fri proces", `retsplejeloven|lov om rettens pleje`, false},
	{"varetægtsfængsling", `retsplejeloven|lov om rettens pleje`, false},
	{"arbejdsskade", `arbejdsskade`, false},
	{"arbejdsmiljø", `lov om arbejdsmiljø$`, false},
	{"boligstøtte", `boligstøtte|individuel boligstøtte`, false},
	// A reader who names the document.
	{"færdselsloven", `af færdselsloven$`, false},
	{"straffeloven", `af straffeloven$`, false},
	{"barnets lov", `barnets lov`, false},
	{"lov om social service", `lov om social service`, false},
	{"sygedagpengeloven", `lov om sygedagpenge`, false},
	{"bekendtgørelse om jagttegn", `^bekendtgørelse om jagttegn`, true},
	{"hundelovens forbudsordning", `forbudsordning`, true},
	{"bekendtgørelse om hold af heste", `hold af heste`, true},
}

var heldOut = []judgment{
	{"erstatning for tabt arbejdsfortjeneste", `erstatningsansvar`, false},
	{"bodeling", `ægtefællers økonomiske forhold|ægtefælleskifte`, false},
	{"adoption", `adoptionsloven|lov om adoption`, false},
	{"navneændring", `navneloven|lov om navne`, false},
	{"børnetilskud", `børnetilskud`, false},
	{"dagtilbud", `dagtilbud`, false},
	{"anbringelse uden for hjemmet", `barnets lov`, false},
	{"hjemmehjælp", `social service`, false},
	{"frit sygehusvalg", `sundhedsloven`, false},
	{"kørekort", `færdselsloven`, false},
	{"parkering", `færdselsloven`, false},
	{"skattepligt", `kildeskatteloven|statsskatteloven`, false},
	{"aktieindkomst", `personskatteloven`, false},
	{"arbejdsløshedsdagpenge", `arbejdsløshedsforsikring`, false},
	{"efterløn", `arbejdsløshedsforsikring`, false},
	{"tinglysning af skøde", `tinglysning`, false},
	{"ejerlejlighed", `ejerlejlighed`, false},
	{"andelsbolig", `andelsboligforeninger`, false},
	{"hegn mellem naboer", `hegnsloven|lov om hegn`, false},
	{"strandbeskyttelse", `naturbeskyttelse`, false},
	{"folketingsvalg", `valg til folketinget`, false},
	{"kommunalbestyrelse", `kommunernes styrelse`, false},
	{"vildledende markedsføring", `af lov om markedsføring$`, false},
	{"inkasso", `inkasso`, false},
	{"forældelse af gæld", `forældelse`, false},
	{"rente ved forsinket betaling", `renter ved forsinket betaling|renteloven`, false},
	{"gældsbrev", `gældsbrev`, false},
	{"fuldmagt", `aftaler og andre retshandler`, false},
	{"fremtidsfuldmagt", `fremtidsfuldmagt`, false},
	{"værgemål", `værgemål`, false},
	{"skifte af dødsbo", `skifte af dødsboer|dødsboskifte`, false},
	{"tavshedspligt offentligt ansat", `af forvaltningsloven|straffeloven`, false},
	{"udbud af offentlige kontrakter", `udbudsloven`, false},
	{"lønmodtagers konkurrenceklausul", `ansættelsesklausuler`, false},
	{"elevplads erhvervsuddannelse", `erhvervsuddannelser`, false},
	// Answers that are orders or guidance, not laws: the priors must not bury them.
	{"bygningsreglement brandsikring", `bygningsreglement`, true},
	{"jagttegnsprøve", `^bekendtgørelse om jagttegn`, true},
	{"momsbekendtgørelsen", `momsbekendtgørelsen`, true},
	{"køreprøve", `kørekort|køreprøve`, true},
	{"vejledning om forvaltningsloven", `^vejledning om forvaltningsloven`, true},
	{"vejledning om aktindsigt", `vejledning om.*offentlighedsloven|vejledning om.*aktindsigt`, true},
	{"standardvilkår for husdyrbrug", `husdyrbrug`, true},
	{"hjemmeværnet", `hjemmeværn`, true},
}

var (
	judgeIdx  *Index
	judgeOnce sync.Once
	judgeErr  error
)

func loadJudgeIndex(t *testing.T) *Index {
	if os.Getenv("RETSIN_RANK_JUDGE") == "" {
		t.Skip("set RETSIN_RANK_JUDGE=1 to judge the ranking against the real index")
	}
	judgeOnce.Do(func() {
		path := os.Getenv("RETSIN_INDEX")
		if path == "" {
			// The current artifact's index, as a server keeps it.
			for _, data := range []string{"data", "../../data"} {
				if art, err := artifactdir.Current(data); err == nil {
					if p, err := artifactdir.IndexFor(art); err == nil {
						path = p
						break
					}
				}
			}
		}
		if path == "" {
			judgeErr = fmt.Errorf("no index found: set RETSIN_INDEX")
			return
		}
		judgeIdx, judgeErr = Load(path)
	})
	if judgeErr != nil {
		t.Skip(judgeErr)
	}
	return judgeIdx
}

// relevantFor says whether a document answers a judgment: the law in force for a law
// judgment — not an act amending it, not its Faroese or Greenlandic version, not an order
// putting it into force — and for the others any document in force with that title.
func relevantFor(j judgment, d DocMeta, re *regexp.Regexp) bool {
	t := strings.ToLower(d.Title)
	if strings.Contains(t, "ændring af") || strings.Contains(t, "færøerne") || strings.Contains(t, "grønland") {
		return false
	}
	if !re.MatchString(t) || statusKey(d.Status) != "Valid" {
		return false
	}
	if j.any {
		return d.Kind == "lta" || d.Kind == "mt" || d.Kind == "retsinfo"
	}
	return d.Kind == "lta" && isLawType(d.TypeCode) && !strings.Contains(t, "ikrafttræden")
}

func judge(idx *Index, set []judgment, filtered, verbose bool) (mrr, hit10 float64) {
	for _, j := range set {
		re := regexp.MustCompile("(?i)" + j.want)
		q := Query{Text: j.q}
		if filtered {
			q.Kinds = []string{"lta"}
			q.Statuses = []string{"Valid"}
		}
		hits := distinctPlaces(idx.Rank(q))
		rank := 0
		for i := 0; i < 10 && i < len(hits); i++ {
			if relevantFor(j, hits[i].Doc, re) {
				rank = i + 1
				break
			}
		}
		if rank > 0 {
			mrr += 1 / float64(rank)
			hit10++
		}
		if verbose && rank != 1 {
			top := ""
			for i := 0; i < 3 && i < len(hits); i++ {
				top += fmt.Sprintf(" [%.50s %s]", hits[i].Doc.Title, hits[i].Addr)
			}
			fmt.Printf("  %-34s rank %2d:%s\n", j.q, rank, top)
		}
	}
	n := float64(len(set))
	return mrr / n, hit10 / n
}

func distinctPlaces(hits []Hit) []Hit {
	type place struct{ acc, addr string }
	seen := map[place]bool{}
	var out []Hit
	for _, h := range hits {
		if k := (place{h.Doc.Accession, h.Addr}); !seen[k] {
			seen[k] = true
			out = append(out, h)
		}
	}
	return out
}

// TestRankingJudgments holds the ranking to what the tuning reached, with some room: tuning
// 0.86 / 0.84 and held-out 0.69 / 0.67 MRR, unfiltered / filtered. RETSIN_RANK_VERBOSE=1
// lists every question whose answer was not first.
func TestRankingJudgments(t *testing.T) {
	idx := loadJudgeIndex(t)
	idx.Ranking = nil
	verbose := os.Getenv("RETSIN_RANK_VERBOSE") != ""
	for _, c := range []struct {
		name  string
		set   []judgment
		floor float64
	}{{"tuning", judgments, 0.80}, {"held-out", heldOut, 0.62}} {
		for _, filtered := range []bool{false, true} {
			mrr, hit := judge(idx, c.set, filtered, verbose)
			t.Logf("%-8s filtered=%-5v MRR %.3f hit@10 %.2f", c.name, filtered, mrr, hit)
			if mrr < c.floor {
				t.Errorf("%s (filtered=%v): MRR %.3f, below %.2f", c.name, filtered, mrr, c.floor)
			}
		}
	}
}

// TestRankingSweep is the tool that set DefaultRanking: coordinate descent over coarse values,
// one knob at a time, scored on the tuning set only. Run it after changing what ranking can
// see, and check the held-out set before adopting what it finds.
func TestRankingSweep(t *testing.T) {
	if os.Getenv("RETSIN_RANK_SWEEP") == "" {
		t.Skip("set RETSIN_RANK_SWEEP=1 (with RETSIN_RANK_JUDGE=1) to sweep the ranking")
	}
	idx := loadJudgeIndex(t)
	defer func() { idx.Ranking = nil }()
	score := func(r Ranking) (float64, string) {
		idx.Ranking = &r
		m1, h1 := judge(idx, judgments, false, false)
		m2, h2 := judge(idx, judgments, true, false)
		return (m1 + m2) / 2, fmt.Sprintf("unfiltered MRR %.3f h@10 %.2f | filtered MRR %.3f h@10 %.2f", m1, h1, m2, h2)
	}
	r := DefaultRanking
	best, desc := score(r)
	t.Logf("start %.3f %s", best, desc)
	knobs := []struct {
		name string
		vals []float64
		set  func(*Ranking, float64)
	}{
		{"Bill", []float64{1, .5, .25, .1, .03}, func(r *Ranking, v float64) { r.Bill = v }},
		{"Amending", []float64{1, .5, .25, .1, .03}, func(r *Ranking, v float64) { r.Amending = v }},
		{"Territory", []float64{1, .5, .25, .1, .03}, func(r *Ranking, v float64) { r.Territory = v }},
		{"Consolidated", []float64{1, 1.5, 2, 3, 5, 8}, func(r *Ranking, v float64) { r.Consolidated = v }},
		{"Historic", []float64{1, .5, .25, .1}, func(r *Ranking, v float64) { r.Historic = v }},
		{"Other", []float64{1, .7, .5, .3}, func(r *Ranking, v float64) { r.Other = v }},
		{"About", []float64{0, .2, .4, .7, 1}, func(r *Ranking, v float64) { r.About = v }},
		{"Title", []float64{1, 1.3, 2, 4, 8}, func(r *Ranking, v float64) { r.Title = v }},
		{"Named", []float64{1, 1.3, 2, 4, 8, 16}, func(r *Ranking, v float64) { r.Named = v }},
		{"B", []float64{0, .2, .4, .6, .75}, func(r *Ranking, v float64) { r.B = v }},
		{"Extend", []float64{0, 1}, func(r *Ranking, v float64) { r.Extend = v == 1 }},
	}
	for round := 0; round < 3; round++ {
		for _, k := range knobs {
			for _, v := range k.vals {
				c := r
				k.set(&c, v)
				if s, d := score(c); s > best+1e-9 {
					best, r, desc = s, c, d
					t.Logf("  %s=%v -> %.3f %s", k.name, v, s, d)
				}
			}
		}
		t.Logf("round %d: %.3f %+v", round, best, r)
	}
	idx.Ranking = &r
	m1, _ := judge(idx, heldOut, false, false)
	m2, _ := judge(idx, heldOut, true, false)
	t.Logf("held-out with the result: unfiltered MRR %.3f, filtered MRR %.3f", m1, m2)
}
