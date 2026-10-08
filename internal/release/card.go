package release

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"text/template"

	"github.com/lkarlslund/retsinformation/internal/disclaimer"
)

// CardFile is the dataset card at the repository root, which Hugging Face shows as the
// dataset's page.
const CardFile = "README.md"

//go:embed card.md
var cardSource string

var cardTemplate = template.Must(template.New(CardFile).Funcs(template.FuncMap{"size": humanSize}).Parse(cardSource))

// writeCard writes the card describing release m of repository repo to a temporary file and
// returns its name.
func writeCard(m Manifest, repo string) (string, error) {
	data := struct {
		Manifest
		Repo              string
		XMLFiles, XMLSize int64
		NoLiability       string
	}{Manifest: m, Repo: repo, NoLiability: disclaimer.NoLiability}
	for _, p := range m.XML {
		data.XMLFiles += int64(p.Files)
		data.XMLSize += p.Size
	}
	var b strings.Builder
	if err := cardTemplate.Execute(&b, data); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "card-*.md")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// humanSize is a byte count as the card shows it.
func humanSize(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
