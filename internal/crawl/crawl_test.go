package crawl

import "testing"

func TestELIKind(t *testing.T) {
	cases := map[string]string{
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019": "retsinfo",
		"https://www.retsinformation.dk/eli/lta/2026/789":        "lta",
		"https://www.retsinformation.dk/eli/ft/202523L00005":     "ft",
		"https://www.retsinformation.dk/eli/fob/2024/1234":       "fob",
		"https://www.retsinformation.dk/eli/mt/2024/1":           "mt",
		"https://www.retsinformation.dk/eli/accn/A20240039929":   "accn",
		"https://www.retsinformation.dk/eli/lta/2026/789.rdfa":   "lta",
		"https://example.com/not-an-eli-url":                     "",
	}
	for url, want := range cases {
		if got := eliKind(url); got != want {
			t.Errorf("eliKind(%s) = %q, want %q", url, got, want)
		}
	}
}

// The fetch phase relies entirely on /xml being served for every ELI kind; these
// forms are the ones actually produced by the sitemap and the harvest feed.
func TestXMLURLFor(t *testing.T) {
	const base = "https://www.retsinformation.dk"
	cases := map[string]string{
		"https://www.retsinformation.dk/eli/retsinfo/2026/10019": base + "/eli/retsinfo/2026/10019/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789":        base + "/eli/lta/2026/789/xml",
		"https://www.retsinformation.dk/eli/ft/202523L00005":     base + "/eli/ft/202523L00005/xml",
		"https://www.retsinformation.dk/eli/accn/A20240039929":   base + "/eli/accn/A20240039929/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789/":       base + "/eli/lta/2026/789/xml",
		"https://www.retsinformation.dk/eli/lta/2026/789.rdfa":   base + "/eli/lta/2026/789/xml",
		"https://example.com/nope":                               "",
	}
	for url, want := range cases {
		if got := xmlURLFor(base, url); got != want {
			t.Errorf("xmlURLFor(%s)\n got %s\nwant %s", url, got, want)
		}
	}
}

func TestNormalizeDate(t *testing.T) {
	cases := map[string]string{
		"2026-09-17":           "2026-09-17",
		"2026-09-17T00:00:00Z": "2026-09-17",
		"17-09-2026":           "2026-09-17",
		"2026-09":              "2026-09",
		"":                     "",
	}
	for in, want := range cases {
		if got := normalizeDate(in); got != want {
			t.Errorf("normalizeDate(%q) = %q, want %q", in, got, want)
		}
	}
}
