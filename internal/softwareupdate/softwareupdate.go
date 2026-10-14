// Package softwareupdate asks whether a newer build of the software has been published than the
// one running, and downloads it when asked to. It never replaces the running program: a downloaded
// build is kept in the data directory, and the program started next runs the newest one there.
//
// A build is named by the commit it was built from, and the published build is the release tag
// the nightly workflow moves to each commit it builds. GitHub's compare API says how many commits
// the tag is ahead of the running commit, which is the answer and its size in one request.
package softwareupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Checker compares the running build with the published one.
type Checker struct {
	Repo     string // owner/name on GitHub
	Tag      string // the published build's release tag
	Revision string // the commit the running binary was built from
	APIBase  string // https://api.github.com when empty
	// DownloadBase is where the published build's files are; the release's download URL when
	// empty.
	DownloadBase string
	UserAgent    string
	Client       *http.Client
}

// URL is the page the published build is downloaded from.
func (c *Checker) URL() string {
	return "https://github.com/" + c.Repo + "/releases/tag/" + url.PathEscape(c.Tag)
}

// Behind says how many commits the published build is ahead of the running one: 0 when the
// running build is the published one or newer.
func (c *Checker) Behind(ctx context.Context) (int, error) {
	base := c.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	u := fmt.Sprintf("%s/repos/%s/compare/%s...%s?per_page=1", strings.TrimRight(base, "/"), c.Repo,
		url.PathEscape(c.Revision), url.PathEscape(c.Tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return 0, fmt.Errorf("GitHub does not know commit %s, which this build was made from", short(c.Revision))
	case resp.StatusCode != http.StatusOK:
		return 0, fmt.Errorf("GitHub answered %s", resp.Status)
	}
	var cmp struct {
		Status  string `json:"status"`
		AheadBy int    `json:"ahead_by"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&cmp); err != nil {
		return 0, err
	}
	// "ahead" and "diverged" both mean the published build holds commits this one does not.
	if cmp.Status == "ahead" || cmp.Status == "diverged" {
		return cmp.AheadBy, nil
	}
	return 0, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
