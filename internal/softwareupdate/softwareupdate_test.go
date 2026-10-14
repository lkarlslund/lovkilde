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
		case "/repos/o/n/releases/latest":
			fmt.Fprint(w, `{"tag_name":"r00042"}`)
		case "/repos/o/n/compare/old...r00042":
			fmt.Fprint(w, `{"status":"ahead","ahead_by":3}`)
		case "/repos/o/n/compare/same...r00042":
			fmt.Fprint(w, `{"status":"identical","ahead_by":0}`)
		case "/repos/o/n/compare/newer...r00042":
			fmt.Fprint(w, `{"status":"behind","ahead_by":0,"behind_by":2}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	for rev, want := range map[string]int{"old": 3, "same": 0, "newer": 0} {
		c := &Checker{Repo: "o/n", Revision: rev, APIBase: srv.URL}
		if got, err := c.Latest(context.Background()); err != nil || got.Behind != want || got.Tag != "r00042" {
			t.Errorf("%s: %+v, %v", rev, got, err)
		}
	}
	c := &Checker{Repo: "o/n", Revision: "local", APIBase: srv.URL}
	if _, err := c.Latest(context.Background()); err == nil {
		t.Error("a commit GitHub does not know was compared")
	}
}
