package httpapi

import (
	"github.com/saidovux/remna-quota/internal/bundle"
	"time"
)

// Pointers distinguish an explicit zero quota (unlimited) from an accidentally
// omitted or null quota; malformed requests must never grant unlimited traffic.
type putInput struct {
	DeviceLimit       *int         `json:"device_limit"`
	Name              string       `json:"name"`
	ExternalRef       string       `json:"external_ref"`
	ExpectedRevision  *int64       `json:"expected_revision"`
	Username          *string      `json:"username"`
	Enabled           *bool        `json:"enabled"`
	Parts             *[]partInput `json:"parts"`
	RenewalPriceMinor *int64       `json:"renewal_price_minor"`
	RenewalPeriodDays *int         `json:"renewal_period_days"`
}
type partInput struct {
	Key           string    `json:"key"`
	Label         string    `json:"label"`
	Provider      string    `json:"provider"`
	Profile       string    `json:"profile"`
	LimitBytes    *int64    `json:"limit_bytes"`
	ExpiresAt     time.Time `json:"expires_at"`
	Enabled       *bool     `json:"enabled"`
	ResetStrategy string    `json:"reset_strategy"`
}

func (in putInput) request() (bundle.PutRequest, bool) {
	if in.Username == nil || in.Enabled == nil || in.Parts == nil {
		return bundle.PutRequest{}, false
	}
	r := bundle.PutRequest{Name: in.Name, ExternalRef: in.ExternalRef, ExpectedRevision: in.ExpectedRevision, Username: *in.Username, Enabled: *in.Enabled, Parts: make([]bundle.PartRequest, 0, len(*in.Parts))}
	r.DeviceLimit = in.DeviceLimit
	r.RenewalPriceMinor = in.RenewalPriceMinor
	r.RenewalPeriodDays = in.RenewalPeriodDays
	for _, p := range *in.Parts {
		if p.LimitBytes == nil || p.Enabled == nil {
			return bundle.PutRequest{}, false
		}
		r.Parts = append(r.Parts, bundle.PartRequest{Key: p.Key, Label: p.Label, Provider: p.Provider, Profile: p.Profile, LimitBytes: *p.LimitBytes, ExpiresAt: p.ExpiresAt, Enabled: *p.Enabled, ResetStrategy: p.ResetStrategy})
	}
	return r, true
}
