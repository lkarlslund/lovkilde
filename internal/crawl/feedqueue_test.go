package crawl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lkarlslund/lovkilde/internal/store"
)

// TestFeedChangesQueueWhatWasFetchedBeforeThem: the sitemap's lastmod does not move for every
// change the feed reports, so a held document the feed says changed after our last fetch has
// to be fetched again — XML and body payload — while one fetched after the change was first
// seen already holds it, and a refetch that brought identical bytes must not queue it forever.
func TestFeedChangesQueueWhatWasFetchedBeforeThem(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	at := func(daysAgo int) string { return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.RFC3339) }
	day := func(daysAgo int) string { return time.Now().UTC().AddDate(0, 0, -daysAgo).Format(time.DateOnly) }

	for _, d := range []struct{ acc, url, held string }{
		{"A1", "https://retsinformation.dk/eli/lta/2020/1", at(5)}, // fetched before the change was seen
		{"B2", "https://retsinformation.dk/eli/lta/2020/2", at(1)}, // fetched after it
	} {
		if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: d.url, Kind: "lta", State: store.StateFetched, Accession: d.acc, Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := f.st.PutAccession(ctx, store.Accession{Accession: d.acc, ELIURL: d.url, Hash: "h", ChangedAt: d.held}); err != nil {
			t.Fatal(err)
		}
		// The body queue keys by the configured site origin, not the sitemap's spelling.
		if err := f.st.PutBody(ctx, store.Body{ELIURL: f.c.bodyKey(d.url), Kind: "lta", State: store.StateFetched}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.st.PutChanges(ctx, []store.Change{
		{Date: day(3), Accession: "A1", Reason: "DocumentMetadataChanged", SeenAt: at(3)},
		{Date: day(3), Accession: "B2", Reason: "DocumentMetadataChanged", SeenAt: at(3)},
	}); err != nil {
		t.Fatal(err)
	}

	if err := f.c.QueueFeedChanges(ctx); err != nil {
		t.Fatal(err)
	}
	state := func(url string) (store.State, store.State) {
		row, _, _ := f.st.GetInventory(ctx, url)
		b, _, _ := f.st.GetBody(ctx, f.c.bodyKey(url))
		return row.State, b.State
	}
	if k := f.c.bodyKey("https://retsinformation.dk/eli/lta/2020/1"); !strings.HasPrefix(k, f.c.cfg.SiteBase) {
		t.Fatalf("body key %q is not under the site origin %q", k, f.c.cfg.SiteBase)
	}
	if doc, body := state("https://retsinformation.dk/eli/lta/2020/1"); doc != store.StateStale || body != store.StateStale {
		t.Errorf("fetched before the change: document %s, body %s; want both stale", doc, body)
	}
	if doc, body := state("https://retsinformation.dk/eli/lta/2020/2"); doc != store.StateFetched || body != store.StateFetched {
		t.Errorf("fetched after the change: document %s, body %s; want both left alone", doc, body)
	}

	// The refetch brings identical bytes: ChangedAt stays old, FetchedAt moves, and the
	// change is not queued again.
	if err := f.st.PutInventory(ctx, store.Inventory{ELIURL: "https://retsinformation.dk/eli/lta/2020/1", Kind: "lta", State: store.StateFetched, Accession: "A1", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.PutAccession(ctx, store.Accession{Accession: "A1", ELIURL: "https://retsinformation.dk/eli/lta/2020/1", Hash: "h", ChangedAt: at(5), FetchedAt: at(0)}); err != nil {
		t.Fatal(err)
	}
	if err := f.c.QueueFeedChanges(ctx); err != nil {
		t.Fatal(err)
	}
	if doc, _ := state("https://retsinformation.dk/eli/lta/2020/1"); doc != store.StateFetched {
		t.Errorf("after a refetch the document is %s again: it would be fetched on every run", doc)
	}
}
