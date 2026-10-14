package eula

import (
	"testing"

	"github.com/lkarlslund/lovkilde/internal/settings"
)

func TestAcceptanceSurvivesReopeningAndNotNewTerms(t *testing.T) {
	dir := t.TempDir()
	if Open(dir).Accepted() {
		t.Fatal("accepted before anyone accepted")
	}
	if err := Open(dir).Accept(); err != nil {
		t.Fatal(err)
	}
	if !Open(dir).Accepted() {
		t.Fatal("the acceptance did not survive reopening")
	}
	settings.Open(dir).Put(section, saved{Version: "0"})
	if Open(dir).Accepted() {
		t.Error("an acceptance of other terms counts")
	}
	var none *Store
	if none.Accepted() || none.Accept() != nil {
		t.Error("a nil store is not empty and quiet")
	}
}
