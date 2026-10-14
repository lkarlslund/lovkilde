package compiled

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lkarlslund/retsinformation/internal/lexdania"
)

var updateModel = flag.Bool("update-model", false, "record the stored-model fingerprint of testdata/models after bumping ModelVersion")

// modelFingerprintFile records which document model the sample compiles to, and under which
// version: "model N" and "sha256 HEX".
const modelFingerprintFile = "testdata/model-fingerprint.txt"

// TestModelVersionCoversTheStoredModel is what lets a server read an artifact another build wrote.
// The artifact is accepted on its model version alone, so the version has to change whenever what
// a document compiles to changes. A committed sample of real documents — laws, orders, amending
// acts, parliamentary and ombudsman documents, XML bodies and HTML-only payloads — is compiled
// through the path `lovkilde-update artifact build` takes, and the stored JSON is hashed. A different hash
// under the same version fails here, naming the constant to bump.
func TestModelVersionCoversTheStoredModel(t *testing.T) {
	a, err := lexdania.Scan(t.Context(), "testdata/models")
	if err != nil {
		t.Fatal(err)
	}
	var accessions []string
	for _, e := range a.Entries() {
		if e.Accession != "" {
			accessions = append(accessions, e.Accession)
		}
	}
	sort.Strings(accessions)
	if len(accessions) < 30 {
		t.Fatalf("the model sample holds %d documents; it should hold the committed sample", len(accessions))
	}
	h := sha256.New()
	for _, acc := range accessions {
		doc, _, ok := a.Open(acc)
		if !ok {
			t.Fatalf("%s: not readable from the sample", acc)
		}
		payload, err := json.Marshal(doc.Model())
		if err != nil {
			t.Fatalf("%s: %v", acc, err)
		}
		fmt.Fprintf(h, "%s %d\n", acc, len(payload))
		h.Write(payload)
	}
	got := hex.EncodeToString(h.Sum(nil))

	if *updateModel {
		body := fmt.Sprintf("model %d\nsha256 %s\n", ModelVersion, got)
		if err := os.WriteFile(modelFingerprintFile, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(modelFingerprintFile)
	if err != nil {
		t.Fatalf("%v: record it with `go test ./internal/compiled -run TestModelVersionCoversTheStoredModel -update-model`", err)
	}
	var version int
	var want string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "model "); ok {
			version, _ = strconv.Atoi(v)
		}
		if v, ok := strings.CutPrefix(line, "sha256 "); ok {
			want = v
		}
	}
	switch {
	case version != ModelVersion:
		t.Fatalf("ModelVersion is %d and %s records model %d: record the new model's fingerprint with `go test ./internal/compiled -run TestModelVersionCoversTheStoredModel -update-model`",
			ModelVersion, modelFingerprintFile, version)
	case got != want:
		t.Fatalf("documents compile to a different stored model than model %d recorded (%s, now %s). An artifact built by an earlier build would be served as if it held this model: bump compiled.ModelVersion, then record the fingerprint with `go test ./internal/compiled -run TestModelVersionCoversTheStoredModel -update-model`",
			ModelVersion, want[:12], got[:12])
	}
}

// TestAnArtifactIsReadByModelNotBuild: a server built from another commit reads an artifact holding
// its own model version, and refuses one holding another, saying which side to rebuild.
func TestAnArtifactIsReadByModelNotBuild(t *testing.T) {
	art := &Artifact{path: "artifact.bin", Header: ArtifactHeader{Build: "0123456789abcdef", Model: ModelVersion}}
	restore := CurrentBuild
	defer func() { CurrentBuild = restore }()
	CurrentBuild = func() (BuildStamp, bool) { return BuildStamp{Revision: "fedcba9876543210"}, true }
	if err := art.CheckModel(); err != nil {
		t.Errorf("an artifact of this model from another build was refused: %v", err)
	}
	art.Header.Model = 4
	if err := art.checkModel(5); err == nil || !strings.Contains(err.Error(), "Rebuild it with `lovkilde-update artifact build`") {
		t.Errorf("an artifact of an older model was not refused for rebuilding: %v", err)
	}
	if err := art.checkModel(3); err == nil || !strings.Contains(err.Error(), "Rebuild and restart the server") {
		t.Errorf("an artifact of a newer model was not refused for the server: %v", err)
	}
	art.Header.Model = 0
	if err := art.CheckModel(); err == nil {
		t.Error("an artifact without a model version from another build was accepted")
	}
}
