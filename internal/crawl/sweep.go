package crawl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lkarlslund/lovkilde/internal/store"
)

// SweepRecord is what a collection pass leaves behind: when it pulled, and a
// fingerprint of what it could not get.
//
// It is deliberately an account of an attempt, not a certificate of completeness. The
// alternative -- recording nothing until a pass finishes with every queue drained --
// throws away the true statement "we pulled today and these 14 documents would not
// come", which is the statement a reader or an operator actually needs. Perfection is
// not available from a remote site, so the record reports what happened instead.
type SweepRecord struct {
	PulledAt time.Time
	store.Issues

	// IssuesKnown is false for a record written before issues were counted, where the
	// honest rendering is that the problems are unknown rather than that there were
	// none.
	IssuesKnown bool
}

// String is the one-line form carried by both the state store and the published file.
//
// It is written out in full -- counts even when they are zero -- rather than as the
// compact human form Issues.String() uses, because the reader of this string has to
// tell "nothing was outstanding" from "nobody counted", and a word like "clean" cannot
// carry both.
func (r SweepRecord) String() string {
	line := r.PulledAt.UTC().Format(time.RFC3339)
	if !r.IssuesKnown {
		return line
	}
	line += fmt.Sprintf(" owed=%d failed=%d", r.Owed, r.Failed)
	if r.Digest != "" {
		line += " issues=" + r.Digest
	}
	return line
}

// ParseSweepRecord reads the form String writes, the older "stalled" for failed, and the
// older bare timestamp that predated the issues field.
func ParseSweepRecord(v string) (SweepRecord, error) {
	fields := strings.Fields(strings.TrimSpace(v))
	if len(fields) == 0 {
		return SweepRecord{}, errors.New("empty sweep record")
	}
	ts, err := time.Parse(time.RFC3339, fields[0])
	if err != nil {
		// A record that will not parse must not read as the zero time, which an age
		// comparison would call either ancient or brand new depending on the sign.
		return SweepRecord{}, fmt.Errorf("parsing sweep timestamp %q: %w", fields[0], err)
	}
	rec := SweepRecord{PulledAt: ts}
	for _, f := range fields[1:] {
		k, val, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		switch k {
		case "owed":
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return SweepRecord{}, fmt.Errorf("parsing sweep record %q: %w", f, err)
			}
			rec.Owed, rec.IssuesKnown = n, true
		case "failed", "stalled": // stalled is what records said before failures were dated
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return SweepRecord{}, fmt.Errorf("parsing sweep record %q: %w", f, err)
			}
			rec.Failed, rec.IssuesKnown = n, true
		case "issues":
			rec.Digest, rec.IssuesKnown = val, true
		}
	}
	return rec, nil
}

// PublishSweepRecord writes the record for readers that cannot open the state store.
//
// The store row is the authority, but the store is locked to one process, so another
// reader -- the artifact build -- cannot have it while a crawl runs. This file
// is the same fact in a form anything can read; nothing else should be derived from it,
// and if the two ever disagree the store is right.
func PublishSweepRecord(path string, rec SweepRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(rec.String()+"\n"), 0o644)
}

// ReadSweepRecord reports the record the last collection pass published. found is false
// when no pass has published one, which a reader states rather than filling in from
// file mtimes: a bulk crawl touches every file in the corpus at once, so file dates say
// nothing about coverage.
func ReadSweepRecord(path string) (SweepRecord, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return SweepRecord{}, false, nil
	}
	if err != nil {
		return SweepRecord{}, false, err
	}
	rec, err := ParseSweepRecord(string(b))
	if err != nil {
		return SweepRecord{}, false, err
	}
	return rec, true, nil
}
