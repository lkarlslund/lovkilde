package config

import (
	"testing"
	"time"
)

// The pacing constants are the terms-of-service guardrails. They used to be
// flags checked by Validate; now that they are constants, these assertions are
// what stop an edit from quietly allowing an aggressive crawl.
func TestPublishedLimitsHold(t *testing.T) {
	if HarvestInterval < HarvestLimit {
		t.Errorf("harvest interval %s breaches the published %s limit", HarvestInterval, HarvestLimit)
	}
	if DefaultDocumentFloor <= 0 {
		t.Error("default document floor must be positive")
	}
	if SitemapInterval <= 0 {
		t.Error("sitemap interval must be positive")
	}
}

func TestDefaultsValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestDocumentFloorIsADefaultNotALimit(t *testing.T) {
	if Default().DocumentFloor != DefaultDocumentFloor {
		t.Errorf("default floor = %s, want %s", Default().DocumentFloor, DefaultDocumentFloor)
	}
	// Anything the operator asks for is honoured; the service's own 429 is the
	// feedback, and the learner slows down on it.
	for _, d := range []time.Duration{time.Millisecond, 50 * time.Millisecond, time.Second} {
		c := Default()
		c.DocumentFloor = d
		if err := c.Validate(); err != nil {
			t.Errorf("floor %s rejected: %v", d, err)
		}
	}
	// A floor that would leave requests unpaced is a typo, not a choice.
	c := Default()
	c.DocumentFloor = 0
	if err := c.Validate(); err == nil {
		t.Error("a zero document floor must be rejected")
	}
}

func TestValidateRejectsBrokenEndpoints(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"empty site base":    func(c *Config) { c.SiteBase = "" },
		"empty harvest base": func(c *Config) { c.HarvestBase = "" },
		"zero timeout":       func(c *Config) { c.RequestTimeout = 0 },
	} {
		c := Default()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}
