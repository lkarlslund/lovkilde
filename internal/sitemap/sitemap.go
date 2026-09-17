// Package sitemap enumerates the ELI sitemap, the official channel that lists
// every canonical ELI resource URI on retsinformation.dk together with its last
// modification date. This is the bulk inventory source: the harvesting service
// only covers a rolling 10-day window, so the sitemap is what makes a complete
// local mirror possible.
//
// Layout observed in production:
//
//	GET /eli/sitemap.xml            -> <sitemapindex> with ?page=N children
//	GET /eli/sitemap.xml?page=N     -> <urlset> with up to 10000 <url> entries
//
// ~21 pages times ~10000 entries, so roughly 200k resources are discoverable
// with only ~22 requests. That is the cheapest possible full inventory and the
// right thing to run before any document download.
package sitemap

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
)

// Entry is one ELI resource with its advertised last-modification date.
type Entry struct {
	Loc     string
	LastMod time.Time
}

// API is a client for the ELI sitemap.
type API struct {
	site   string
	client *rihttp.Client
}

// New returns a sitemap client.
func New(cfg config.Config, client *rihttp.Client) *API {
	return &API{site: cfg.SiteBase, client: client}
}

type sitemapIndex struct {
	XMLName  xml.Name `xml:"sitemapindex"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

type urlSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
}

// Index returns the URLs of all paginated sub-sitemaps.
func (a *API) Index(ctx context.Context) ([]string, error) {
	raw, err := a.client.GetJSON(ctx, limiter.BucketSitemap, a.site+"/eli/sitemap.xml", "application/xml")
	if err != nil {
		return nil, fmt.Errorf("sitemap index: %w", err)
	}
	var idx sitemapIndex
	if err := xml.Unmarshal(raw, &idx); err != nil {
		return nil, fmt.Errorf("sitemap index: decode: %w", err)
	}
	out := make([]string, 0, len(idx.Sitemaps))
	for _, s := range idx.Sitemaps {
		out = append(out, a.canonical(s.Loc))
	}
	return out, nil
}

// Page fetches one sub-sitemap.
func (a *API) Page(ctx context.Context, pageURL string) ([]Entry, error) {
	raw, err := a.client.GetJSON(ctx, limiter.BucketSitemap, pageURL, "application/xml")
	if err != nil {
		return nil, fmt.Errorf("sitemap page %s: %w", pageURL, err)
	}
	var set urlSet
	if err := xml.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("sitemap page %s: decode: %w", pageURL, err)
	}
	entries := make([]Entry, 0, len(set.URLs))
	for _, u := range set.URLs {
		e := Entry{Loc: a.canonical(u.Loc)}
		if s := strings.TrimSpace(u.LastMod); s != "" {
			// lastmod is a date (2026-09-17) but may carry a time offset.
			if t, err := time.Parse(time.DateOnly, s); err == nil {
				e.LastMod = t
			} else if t, err := time.Parse(time.RFC3339, s); err == nil {
				e.LastMod = t
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// Inventory walks the whole sitemap index and streams every entry, reporting
// progress per page. The callback's error aborts the walk.
func (a *API) Inventory(ctx context.Context, onBatch func(page int, entries []Entry) error) error {
	pages, err := a.Index(ctx)
	if err != nil {
		return err
	}
	for i, p := range pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := a.Page(ctx, p)
		if err != nil {
			return err
		}
		if err := onBatch(i+1, entries); err != nil {
			return err
		}
	}
	return nil
}

// canonical rewrites a sitemap URL onto the configured site origin and the www
// host. The sitemap publishes retsinformation.dk, which 301-redirects to the
// www host; rewriting avoids a wasted redirect per URL.
func (a *API) canonical(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Host), "www.")
	if host != hostOf(a.site) {
		// A foreign host: leave the URL exactly as published.
		return u
	}
	parsed.Scheme = schemeOf(a.site)
	parsed.Host = hostOf(a.site)
	return parsed.String()
}

func hostOf(base string) string {
	p, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(p.Host)
}

func schemeOf(base string) string {
	p, err := url.Parse(base)
	if err != nil {
		return "https"
	}
	return p.Scheme
}
