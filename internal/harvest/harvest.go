// Package harvest talks to Civilstyrelsens "høste" (harvesting) service, the
// officially documented channel for staying current with changed legislation.
//
// Published constraints (see https://www.retsinformation.dk/api and the service's
// own OpenAPI description at https://api.retsinformation.dk/swagger/v1/swagger.json):
//   - The service may be called at most once per 10 seconds; exceeding this
//     returns HTTP 429 with the text "Servicen er kaldt for ofte. Den må kun
//     kaldes en gang pr. 10 sekunder."
//   - A date argument must fall within the last 10 days.
//   - The intended usage is one call per day ("I en almindelig situation kaldes
//     denne service en gang i døgnet").
//
// Because the window is only 10 days wide, the service is a change feed, not a
// bulk source: the client must itself keep track of being up to date, which is
// exactly what the manifest in internal/store does.
package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/lkarlslund/retsinformation/internal/config"
	"github.com/lkarlslund/retsinformation/internal/limiter"
	"github.com/lkarlslund/retsinformation/internal/rihttp"
)

// ReasonForChange enumerates why the service reported a document.
type ReasonForChange string

const (
	NewDocument             ReasonForChange = "NewDocument"
	Consolidated            ReasonForChange = "Consolidated"
	DocumentChanged         ReasonForChange = "DocumentChanged"
	DocumentMetadataChanged ReasonForChange = "DocumentMetadataChanged"
	DocumentReplaced        ReasonForChange = "DocumentReplaced"
	DocumentRescinded       ReasonForChange = "DocumentRescinded"
	DocumentAbolished       ReasonForChange = "DocumentAbolished"
	HistoricalVersion       ReasonForChange = "HistoricalVersion"
)

// Document is one entry of /v1/Documents.
type Document struct {
	DocumentID       string          `json:"documentId"`
	Accessionsnummer string          `json:"accessionsnummer"`
	ReasonForChange  ReasonForChange `json:"reasonForChange"`
	ChangeDate       string          `json:"changeDate"`
	DocumentType     DocumentType    `json:"documentType"`
	// Href is an ELI accession URL, typically .../eli/accn/<accn>/xml.
	Href   string  `json:"href"`
	Images []Image `json:"images"`
}

// DocumentType is the document's short type code, e.g. "LBK H" (lovbekendtgørelse).
type DocumentType struct {
	ShortName string `json:"shortName"`
	ID        int    `json:"id"`
}

// Image references an image embedded in a document.
type Image struct {
	Source  string `json:"source"`
	AltText string `json:"altText"`
}

// API is a client for the harvesting service.
type API struct {
	base   string
	client *rihttp.Client
}

// New returns a harvesting-service client.
func New(cfg config.Config, client *rihttp.Client) *API {
	return &API{base: cfg.HarvestBase, client: client}
}

// MaxWindowDays is how far back the service will answer.
const MaxWindowDays = 10

// ForDay fetches the changes registered for a single calendar day. day must be
// within the last MaxWindowDays days or the service returns 400.
func (a *API) ForDay(ctx context.Context, day time.Time) ([]Document, error) {
	u := fmt.Sprintf("%s/v1/Documents?date=%s", a.base, day.UTC().Format("2006-01-02"))
	raw, err := a.client.GetJSON(ctx, limiter.BucketHarvest, u, "application/json")
	if err != nil {
		return nil, fmt.Errorf("harvest %s: %w", day.Format(time.DateOnly), err)
	}
	var docs []Document
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, fmt.Errorf("harvest %s: decode: %w", day.Format(time.DateOnly), err)
	}
	return docs, nil
}

// Days returns each day from newest to oldest that still falls inside the
// service's window, for backfilling after downtime.
func Days(now time.Time, count int) []time.Time {
	if count > MaxWindowDays {
		count = MaxWindowDays
	}
	out := make([]time.Time, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, now.UTC().AddDate(0, 0, -i))
	}
	return out
}

// Available reports whether the service is reachable and not throttling.
func (a *API) Available(ctx context.Context) error {
	u := a.base + "/v1/Documents"
	if _, err := url.Parse(u); err != nil {
		return err
	}
	_, err := a.client.GetJSON(ctx, limiter.BucketHarvest, u, "application/json")
	return err
}
