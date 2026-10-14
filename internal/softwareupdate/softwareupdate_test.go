package softwareupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckSaysHowFarBehindTheRunningBuildIs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/n/compare/old...nightly":
			fmt.Fprint(w, `{"status":"ahead","ahead_by":3}`)
		case "/repos/o/n/compare/same...nightly":
			fmt.Fprint(w, `{"status":"identical","ahead_by":0}`)
		case "/repos/o/n/compare/newer...nightly":
			fmt.Fprint(w, `{"status":"behind","ahead_by":0,"behind_by":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for rev, want := range map[string]int{"old": 3, "same": 0, "newer": 0} {
		c := &Checker{Repo: "o/n", Tag: "nightly", Revision: rev, APIBase: srv.URL}
		if got, err := c.Behind(context.Background()); err != nil || got != want {
			t.Errorf("%s: %d, %v", rev, got, err)
		}
	}
	c := &Checker{Repo: "o/n", Tag: "nightly", Revision: "local", APIBase: srv.URL}
	if _, err := c.Behind(context.Background()); err == nil {
		t.Error("a commit GitHub does not know was compared")
	}
}
