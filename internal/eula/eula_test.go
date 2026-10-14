package eula

import (
	"os"
	"path/filepath"
	"testing"
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
	os.WriteFile(filepath.Join(dir, File), []byte("0\n2020-01-01T00:00:00Z\n"), 0o644)
	if Open(dir).Accepted() {
		t.Error("an acceptance of other terms counts")
	}
	var none *Store
	if none.Accepted() || none.Accept() != nil {
		t.Error("a nil store is not empty and quiet")
	}
}
