package config

import (
	"testing"
	"time"
)

func TestDefaultsValidate(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestTheFloorIsADefaultNotALimit(t *testing.T) {
	if Default().Floor != DefaultFloor {
		t.Errorf("default floor = %s, want %s", Default().Floor, DefaultFloor)
	}
	// Anything the operator asks for is honoured; the service's own 429 is the
	// feedback, and the learner slows down on it.
	for _, d := range []time.Duration{time.Millisecond, 50 * time.Millisecond, time.Second} {
		c := Default()
		c.Floor = d
		if err := c.Validate(); err != nil {
			t.Errorf("floor %s rejected: %v", d, err)
		}
	}
	// A floor that would leave requests unpaced is a typo, not a choice.
	c := Default()
	c.Floor = 0
	if err := c.Validate(); err == nil {
		t.Error("a zero floor must be rejected")
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
