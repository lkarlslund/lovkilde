package main

import "testing"

func TestExposedAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8787": false,
		"[::1]:8787":     false,
		"localhost:8787": false,
		"0.0.0.0:8787":   true,
		":8787":          true,
		"10.0.0.5:8787":  true,
	} {
		if got := exposedAddr(addr); got != want {
			t.Errorf("exposedAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
