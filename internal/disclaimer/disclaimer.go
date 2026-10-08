// Package disclaimer is what every surface of the mirror says about itself: that it is not the
// official Retsinformation, that its texts are processed by machine and can be wrong, above all
// where amending acts have been worked into a law, and that use is at the user's own risk. One
// wording, kept here, so the website, the MCP answers, the command help, the README and the
// dataset card cannot drift apart.
package disclaimer

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
