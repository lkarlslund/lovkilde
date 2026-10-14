package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSectionsShareTheFileAndOldFilesMoveIn(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "checks.json"), []byte(`{"dataset":{"every":"ugentligt"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "apikey"), []byte("abc\n"), 0o600)

	f := Open(dir)
	if err := f.Put("eula", map[string]string{"version": "1"}); err != nil {
		t.Fatal(err)
	}
	var key struct{ SHA256 string }
	var checks map[string]struct{ Every string }
	var eula struct{ Version string }
	if !f.Get("apikey", &key) || key.SHA256 != "abc" {
		t.Errorf("apikey section %+v", key)
	}
	if !f.Get("checks", &checks) || checks["dataset"].Every != "ugentligt" {
		t.Errorf("checks section %+v", checks)
	}
	if !f.Get("eula", &eula) || eula.Version != "1" {
		t.Errorf("eula section %+v", eula)
	}
	for _, old := range []string{"checks.json", "apikey"} {
		if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
			t.Errorf("%s was not removed after moving into %s", old, Name)
		}
	}
	if Open(dir) != f {
		t.Error("two openers of one directory got different files")
	}
	if err := f.Put("apikey", nil); err != nil || f.Get("apikey", &key) {
		t.Error("a removed section is still there")
	}
}
