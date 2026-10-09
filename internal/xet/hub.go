package xet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrNotXet is returned for a file the Hub does not keep in Xet storage.
var ErrNotXet = errors.New("not stored in Xet")

// Hub is a Hugging Face dataset repository whose files are read through Xet storage.
type Hub struct {
	Repo     string // "owner/name"
	Revision string // branch, tag or commit; "main" when empty
	BaseURL  string // https://huggingface.co when empty
	Token    string // a Hub token, for a private repository; anonymous when empty
	Client   *http.Client

	mu     sync.Mutex
	cas    string
	access string
	exp    time.Time
}

func (h *Hub) base() string {
	if h.BaseURL == "" {
		return "https://huggingface.co"
	}
	return strings.TrimRight(h.BaseURL, "/")
}

func (h *Hub) rev() string {
	if h.Revision == "" {
		return "main"
	}
	return h.Revision
}

func (h *Hub) client() *http.Client {
	if h.Client == nil {
		return http.DefaultClient
	}
	return h.Client
}

// FileHash is the Xet hash of a file in the repository: the name its content is stored under.
// A file that is not in the repository is an error wrapping fs.ErrNotExist.
func (h *Hub) FileHash(ctx context.Context, name string) (string, error) {
	u := h.base() + "/datasets/" + h.Repo + "/resolve/" + url.PathEscape(h.rev()) + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	// The resolve endpoint redirects to a plain download; the hash is on the redirect itself.
	c := *h.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("%s: %w", u, fs.ErrNotExist)
	case resp.StatusCode >= 400:
		return "", fmt.Errorf("%s: %s", u, resp.Status)
	}
	hash := resp.Header.Get("X-Xet-Hash")
	if !validHash(hash) {
		return "", fmt.Errorf("%s: %w", u, ErrNotXet)
	}
	return hash, nil
}

// token returns the CAS endpoint and a read token for it, asking the Hub for a new one shortly
// before the last one expires.
func (h *Hub) token(ctx context.Context) (cas, access string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.access != "" && time.Until(h.exp) > 30*time.Second {
		return h.cas, h.access, nil
	}
	u := h.base() + "/api/datasets/" + h.Repo + "/xet-read-token/" + url.PathEscape(h.rev())
	var t struct {
		AccessToken string `json:"accessToken"`
		Exp         int64  `json:"exp"`
		CasURL      string `json:"casUrl"`
	}
	if err := h.getJSON(ctx, u, h.Token, &t); err != nil {
		return "", "", fmt.Errorf("a Xet read token: %w", err)
	}
	if t.AccessToken == "" || t.CasURL == "" {
		return "", "", fmt.Errorf("a Xet read token: %s answered without one", u)
	}
	h.cas, h.access, h.exp = strings.TrimRight(t.CasURL, "/"), t.AccessToken, time.Unix(t.Exp, 0)
	return h.cas, h.access, nil
}

// reconstruction is how a file is put together from xorbs.
type reconstruction struct {
	Offset int64              `json:"offset_into_first_range"`
	Terms  []term             `json:"terms"`
	Xorbs  map[string][]fetch `json:"xorbs"`
}

// term is a run of chunks from one xorb, in the order the file holds them.
type term struct {
	Xorb     string     `json:"hash"`
	Unpacked int64      `json:"unpacked_length"`
	Range    chunkRange `json:"range"`
}

// chunkRange is chunk indexes within a xorb, end-exclusive.
type chunkRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// fetch is a signed URL for byte ranges of one xorb.
type fetch struct {
	URL    string `json:"url"`
	Ranges []struct {
		Chunks chunkRange `json:"chunks"`
		Bytes  struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"` // inclusive
		} `json:"bytes"`
	} `json:"ranges"`
}

func (h *Hub) reconstruction(ctx context.Context, hash string) (*reconstruction, error) {
	if !validHash(hash) {
		return nil, fmt.Errorf("%q is not a Xet hash", hash)
	}
	cas, access, err := h.token(ctx)
	if err != nil {
		return nil, err
	}
	var r reconstruction
	if err := h.getJSON(ctx, cas+"/v2/reconstructions/"+hash, access, &r); err != nil {
		return nil, fmt.Errorf("the reconstruction of %s: %w", hash, err)
	}
	if r.Offset != 0 {
		return nil, fmt.Errorf("the reconstruction of %s starts %d bytes into its first term", hash, r.Offset)
	}
	for _, t := range r.Terms {
		if !validHash(t.Xorb) || t.Range.Start < 0 || t.Range.End <= t.Range.Start || t.Unpacked <= 0 {
			return nil, fmt.Errorf("the reconstruction of %s holds a malformed term %+v", hash, t)
		}
	}
	return &r, nil
}

func (h *Hub) getJSON(ctx context.Context, u, bearer string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", strings.SplitN(u, "?", 2)[0], resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(v)
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}
