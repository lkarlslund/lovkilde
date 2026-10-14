package main

import "testing"

func TestBrowserURL(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:8787": "http://127.0.0.1:8787/",
		"0.0.0.0:8787":   "http://127.0.0.1:8787/",
		"[::]:8787":      "http://127.0.0.1:8787/",
		":8787":          "http://127.0.0.1:8787/",
		"[::1]:8787":     "http://[::1]:8787/",
		"halo:8787":      "http://halo:8787/",
	} {
		if got := browserURL(addr); got != want {
			t.Errorf("browserURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

// "lovkilde" alone serves, and takes serve's flags.
func TestNoCommandServes(t *testing.T) {
	root := newRootCmd()
	if root.RunE == nil {
		t.Fatal("lovkilde with no command does nothing")
	}
	for _, name := range []string{"no-browser", "addr", "release"} {
		if root.Flags().Lookup(name) == nil {
			t.Errorf("lovkilde takes no --%s", name)
		}
	}
}
