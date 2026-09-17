// Package config holds the mirror tool's tunables, loaded from a JSON file
// and overridable by environment variables.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultUserAgent identifies the client to the server. Always keep a contact
// address here: Civilstyrelsen asks users not to burden the service, and an
// identifiable client lets them contact us instead of banning us.
const DefaultUserAgent = "retsin-mirror/0.1 (open-data mirror; contact: https://github.com/lkarlslund/retsinformation)"

// Config is the fully-resolved runtime configuration.
type Config struct {
	// DataDir is where the manifest database and downloaded documents live.
	DataDir string `json:"data_dir"`

	// SiteBase is the origin serving the ELI channels and the document API.
	SiteBase string `json:"site_base"`
	// HarvestBase is the dedicated harvesting ("høste") service origin.
	HarvestBase string `json:"harvest_base"`

	// HarvestInterval is the published hard limit of the harvesting service:
	// it may be called only once per 10 seconds. We never go faster than this.
	HarvestInterval Duration `json:"harvest_interval"`
	// DocumentInterval is the minimum gap between requests for documents.
	// Documents are served uncached by the origin, so this is our own courtesy
	// throttle rather than a published limit.
	DocumentInterval Duration `json:"document_interval"`

	// MaxRetries is the number of retries after a throttled or failed request.
	MaxRetries int `json:"max_retries"`
	// BackoffBase doubles from here on each retry, capped at BackoffMax.
	BackoffBase Duration `json:"backoff_base"`
	BackoffMax  Duration `json:"backoff_max"`
	// BackoffJitter randomises retry delays so retries do not synchronise.
	BackoffJitter float64 `json:"backoff_jitter"`

	// RequestTimeout bounds a single HTTP request.
	RequestTimeout Duration `json:"request_timeout"`

	// UserAgent sent on every request.
	UserAgent string `json:"user_agent"`

	// FetchJSON additionally stores the site's JSON representation (full HTML
	// plus rich metadata) for documents whose content actually changed.
	FetchJSON bool `json:"fetch_json"`
}

// Default returns production-sane defaults built around the published limits.
func Default() Config {
	return Config{
		DataDir:          "data",
		SiteBase:         "https://www.retsinformation.dk",
		HarvestBase:      "https://api.retsinformation.dk",
		HarvestInterval:  Duration{12 * time.Second}, // published limit: 1 per 10s
		DocumentInterval: Duration{1200 * time.Millisecond},
		MaxRetries:       6,
		BackoffBase:      Duration{10 * time.Second},
		BackoffMax:       Duration{15 * time.Minute},
		BackoffJitter:    0.3,
		RequestTimeout:   Duration{60 * time.Second},
		UserAgent:        DefaultUserAgent,
		FetchJSON:        true,
	}
}

// Path returns the manifest database path inside DataDir.
func (c Config) ManifestDB() string { return filepath.Join(c.DataDir, "manifest.sqlite") }

// XMLDir returns the directory holding downloaded ELI XML documents.
func (c Config) XMLDir() string { return filepath.Join(c.DataDir, "xml") }

// JSONDir returns the directory holding downloaded JSON representations.
func (c Config) JSONDir() string { return filepath.Join(c.DataDir, "json") }

// Load reads an optional JSON config file over the defaults, then applies
// environment overrides. An empty path means defaults plus overrides only.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config: %w", err)
		}
		// Unknown keys are rejected: a typo in a throttle setting is dangerous.
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := cfg.applyEnv(); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate refuses configurations that would violate the published terms.
func (c Config) Validate() error {
	if c.HarvestBase == "" || c.SiteBase == "" {
		return fmt.Errorf("site_base and harvest_base must be set")
	}
	if c.HarvestInterval.D() < 10*time.Second {
		return fmt.Errorf("harvest_interval is %s, but the harvesting service allows at most one call per 10 seconds", c.HarvestInterval)
	}
	if c.DocumentInterval.D() < 200*time.Millisecond {
		return fmt.Errorf("document_interval of %s is too aggressive for an uncached origin; use at least 200ms", c.DocumentInterval)
	}
	if c.BackoffJitter < 0 || c.BackoffJitter > 1 {
		return fmt.Errorf("backoff_jitter must be between 0 and 1")
	}
	return nil
}

func (c *Config) applyEnv() error {
	envDur := func(name string, d *Duration) error {
		v := os.Getenv(name)
		if v == "" {
			return nil
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		*d = Duration{parsed}
		return nil
	}
	for name, d := range map[string]*Duration{
		"RETSIN_HARVEST_INTERVAL":  &c.HarvestInterval,
		"RETSIN_DOCUMENT_INTERVAL": &c.DocumentInterval,
		"RETSIN_BACKOFF_BASE":      &c.BackoffBase,
		"RETSIN_BACKOFF_MAX":       &c.BackoffMax,
		"RETSIN_REQUEST_TIMEOUT":   &c.RequestTimeout,
	} {
		if err := envDur(name, d); err != nil {
			return err
		}
	}
	if v := os.Getenv("RETSIN_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("RETSIN_USER_AGENT"); v != "" {
		c.UserAgent = v
	}
	return nil
}

// Duration marshals time.Duration as a Go duration string ("1.2s") so config
// files stay readable.
type Duration struct{ time.Duration }

func (d Duration) D() time.Duration { return d.Duration }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}
