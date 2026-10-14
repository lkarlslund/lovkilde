// Package softwareupdate asks whether a newer build of the software has been published than the
// one running, and downloads it when asked to. It never replaces the running program: a downloaded
// build is kept in the data directory, and the program started next runs the newest one there.
//
// The published builds are GitHub releases numbered rNNNNN, the number of commits on main; the
// newest is the repository's latest release. GitHub's compare API says how many commits it is
// ahead of the running commit.
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
	Repo      string // owner/name on GitHub
	Tag       string // the published build's release tag; the latest release when empty
	Revision  string // the commit the running binary was built from
	APIBase   string // https://api.github.com when empty
	UserAgent string
	Client    *http.Client
	// DownloadBase is where the published build's files are; the release's download URL when
	// empty.
	DownloadBase string
}

// Release is the published build as compared with the running one.
type Release struct {
	Tag    string // its release tag, rNNNNN
	Behind int    // how many commits it holds that the running build does not; 0 when not newer
}

// URL is the page the published build is downloaded from.
func (c *Checker) URL() string {
	if c.Tag == "" {
		return "https://github.com/" + c.Repo + "/releases/latest"
	}
	return "https://github.com/" + c.Repo + "/releases/tag/" + url.PathEscape(c.Tag)
}

func (c *Checker) api(ctx context.Context, path string, out any) error {
	base := c.APIBase
	if base == "" {
		base = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return err
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
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

var errNotFound = fmt.Errorf("not found")

// Latest finds the published build and how far it is ahead of the running one.
func (c *Checker) Latest(ctx context.Context) (Release, error) {
	tag := c.Tag
	if tag == "" {
		var rel struct {
			TagName string `json:"tag_name"`
		}
		if err := c.api(ctx, "/repos/"+c.Repo+"/releases/latest", &rel); err != nil {
			if err == errNotFound {
				return Release{}, fmt.Errorf("%s has no published release", c.Repo)
			}
			return Release{}, err
		}
		tag = rel.TagName
	}
	var cmp struct {
		Status  string `json:"status"`
		AheadBy int    `json:"ahead_by"`
	}
	err := c.api(ctx, fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=1", c.Repo, url.PathEscape(c.Revision), url.PathEscape(tag)), &cmp)
	if err == errNotFound {
		return Release{}, fmt.Errorf("GitHub does not know commit %s, which this build was made from", short(c.Revision))
	}
	if err != nil {
		return Release{}, err
	}
	r := Release{Tag: tag}
	// "ahead" and "diverged" both mean the published build holds commits this one does not.
	if cmp.Status == "ahead" || cmp.Status == "diverged" {
		r.Behind = cmp.AheadBy
	}
	return r, nil
}

func short(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}
