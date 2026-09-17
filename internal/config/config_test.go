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
	if DocumentFloor <= 0 {
		t.Error("document floor must be positive")
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

func TestValidateRejectsTooFastDocuments(t *testing.T) {
	c := Default()
	c.DocumentFloor = time.Millisecond
	if err := c.Validate(); err == nil {
		t.Fatal("a document floor below the courtesy minimum must be rejected")
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
