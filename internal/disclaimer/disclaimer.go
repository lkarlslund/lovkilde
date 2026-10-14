// Package disclaimer is what every surface of the mirror says about itself: that it is not the
// official Retsinformation, that its texts are processed by machine and can be wrong, above all
// where amending acts have been worked into a law, and that use is at the user's own risk. One
// wording, kept here, so the website, the MCP answers, the command help, the README and the
// dataset card cannot drift apart.
package disclaimer

import (
	"fmt"
	"strings"
)

// Short is the one-paragraph notice shown with every page and every answer.
const (
	ShortDanish = "Uofficiel kopi – ikke den officielle Retsinformation. Teksterne er hentet automatisk " +
		"fra retsinformation.dk og behandlet maskinelt, og de kan indeholde fejl. Al brug sker på eget " +
		"ansvar og uden nogen form for garanti. Kontrollér altid mod den officielle udgave på " +
		"retsinformation.dk, og søg juridisk rådgivning, før du handler på baggrund af teksten."
	ShortEnglish = "Unofficial copy – not the official Retsinformation. The texts are fetched automatically " +
		"from retsinformation.dk and processed by machine, and they can contain errors. All use is at your " +
		"own risk and without warranty of any kind. Always check against the official version at " +
		"retsinformation.dk, and seek legal advice before acting on the text."
)

// Consolidated is the notice shown with a text consolidated to a date, where the mirror itself
// has worked the amending acts into the law: the part most likely to be wrong.
const (
	ConsolidatedDanish = "Maskinelt konsolideret tekst. Ændringslovene er indarbejdet i loven automatisk af " +
		"dette program, ikke af Civilstyrelsen. Teksten er ikke en officiel lovbekendtgørelse og kan " +
		"indeholde fejl: ændringer kan være udeladt, placeret forkert eller læst forkert, og ikrafttræden " +
		"kan være bedømt forkert. Brug den kun som orientering, på eget ansvar, og kontrollér den mod " +
		"retsinformation.dk."
	ConsolidatedEnglish = "Machine-consolidated text. The amending acts were worked into the law automatically by " +
		"this program, not by Civilstyrelsen. The text is not an official consolidation and can contain " +
		"errors: changes may be missing, misplaced or misread, and dates of entry into force may be " +
		"judged wrongly. Use it for orientation only, at your own risk, and check it against " +
		"retsinformation.dk."
)

// NoLiability is the full statement for the documents that describe the project (the README,
// the dataset card, the release notes).
const NoLiability = "This project is not affiliated with, endorsed by or connected to Civilstyrelsen or " +
	"retsinformation.dk. The software and the data are provided \"as is\", without warranty of any kind, " +
	"express or implied, including fitness for a particular purpose and accuracy or completeness of the " +
	"texts. The authors and contributors accept no liability for any loss or damage of any kind arising " +
	"from the use of, or reliance on, the software or the data. Nothing here is legal advice."

// Terms are what a user accepts before the website or the MCP tools give them anything: the
// notices above at full length, in Danish. TermsVersion names this wording; changing the terms
// means raising it, so everyone who accepted the old wording is asked again.
const (
	TermsVersion = "1"
	TermsTitle   = "Vilkår for brug – læs før du fortsætter"
	TermsAccept  = "Jeg har læst og forstået vilkårene og accepterer dem."
)

// Terms is the terms, one paragraph each, each with its heading.
var Terms = []struct{ Heading, Text string }{
	{"Uofficiel kopi", "Dette er ikke den officielle Retsinformation. Tjenesten har ingen tilknytning til " +
		"Civilstyrelsen, Justitsministeriet eller retsinformation.dk og er ikke godkendt af dem. Kun teksten " +
		"på retsinformation.dk er den gældende."},
	{"Maskinelt behandlede tekster", "Teksterne hentes automatisk fra retsinformation.dk og omdannes " +
		"maskinelt. De kan være ufuldstændige, forældede, forkert opdelt eller forkert gengivet, og nyere " +
		"ændringer kan mangle."},
	{"Maskinel konsolidering", "Når en lov vises, som den lød på en bestemt dato, har dette program selv " +
		"indarbejdet ændringslovene i den. Det er ikke en officiel lovbekendtgørelse. Ændringer kan være " +
		"udeladt, placeret forkert eller læst forkert, og ikrafttræden kan være bedømt forkert."},
	{"Søgning og sprogmodeller", "Søgninger kan overse relevante regler, og svar givet gennem en " +
		"sprogmodel (AI) kan gengive, sammenfatte eller fortolke teksterne forkert."},
	{"Bedste indsats – ingen garanti", "Tjenesten er et frivilligt projekt, der leveres efter bedste " +
		"evne, \"som den er\" og \"som tilgængelig\", uden garanti af nogen art, hverken udtrykkelig eller " +
		"underforstået. Det gælder blandt andet tekstens rigtighed, fuldstændighed og aktualitet, " +
		"tjenestens tilgængelighed og dens egnethed til et bestemt formål. Tjenesten kan ændres eller " +
		"lukkes uden varsel."},
	{"Intet ansvar", "Udviklerne og bidragyderne påtager sig intet ansvar for tab eller skade af nogen " +
		"art, direkte eller indirekte, som følger af brug af eller tillid til tjenesten eller dens tekster, " +
		"i det omfang lovgivningen tillader det."},
	{"Ikke juridisk rådgivning", "Intet her er juridisk rådgivning. Kontrollér altid mod den officielle " +
		"udgave på retsinformation.dk, og søg rådgivning hos en advokat, før du handler eller træffer " +
		"beslutninger på baggrund af teksten."},
	{"Brug på eget ansvar", "Al brug sker på eget ansvar. Accepterer du ikke vilkårene, må du ikke bruge " +
		"tjenesten."},
}

// TermsText is the terms as plain text, for an answer that is not a web page.
func TermsText() string {
	var b strings.Builder
	b.WriteString(TermsTitle + "\n")
	for i, t := range Terms {
		fmt.Fprintf(&b, "\n%d. %s\n%s\n", i+1, t.Heading, t.Text)
	}
	b.WriteString("\n" + TermsAccept + "\n")
	return b.String()
}
