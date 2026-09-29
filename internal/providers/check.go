package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/saidovux/remna-quota/internal/bundle"
)

type CheckResult struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}
type CheckReport struct {
	Ready               bool          `json:"read_checks_passed"`
	Version             string        `json:"version,omitempty"`
	Checks              []CheckResult `json:"checks"`
	WriteScopesVerified bool          `json:"write_scopes_verified"`
}

// Check performs GET requests only. Missing sentinel users are intentional;
// their 404 responses demonstrate route access without reading customer data.
// It cannot certify create/update/enable/disable permissions or node enforcement.
func (p *Remnawave) Check(ctx context.Context) CheckReport {
	return p.check(ctx, true)
}

func (p *Remnawave) CheckReferences(ctx context.Context) CheckReport {
	return p.check(ctx, false)
}

func (p *Remnawave) check(ctx context.Context, managed bool) CheckReport {
	r := CheckReport{Ready: true, Checks: []CheckResult{}}
	add := func(name string, err error, missingOK bool) {
		check := CheckResult{Name: name, OK: err == nil || (missingOK && errors.Is(err, bundle.ErrNotFound))}
		if !check.OK {
			check.Code = "provider_unavailable"
			var access *AccessError
			if errors.As(err, &access) {
				if access.StatusCode == http.StatusUnauthorized {
					check.Code = "invalid_api_token"
				} else {
					check.Code = "missing_api_scope"
				}
			}
			r.Ready = false
		}
		r.Checks = append(r.Checks, check)
	}
	var metadata struct {
		Response struct {
			Version string `json:"version"`
		} `json:"response"`
	}
	err := p.do(ctx, http.MethodGet, "/api/system/metadata", nil, http.StatusOK, &metadata)
	if err == nil {
		r.Version = metadata.Response.Version
		if !strings.HasPrefix(r.Version, "3.") {
			err = bundle.ErrUnavailable
		}
	}
	add("system:metadata", err, false)
	if managed {
		var squads struct {
			Response struct {
				Squads []struct {
					UUID string `json:"uuid"`
				} `json:"internalSquads"`
			} `json:"response"`
		}
		err = p.do(ctx, http.MethodGet, "/api/internal-squads", nil, http.StatusOK, &squads)
		if err == nil && squads.Response.Squads == nil {
			err = bundle.ErrUnavailable
		}
		add("internal-squads:list", err, false)
		if err == nil {
			available := make(map[string]bool)
			for _, s := range squads.Response.Squads {
				available[strings.ToLower(s.UUID)] = true
			}
			keys := make([]string, 0, len(p.profiles))
			for key := range p.profiles {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				check := CheckResult{Name: "profile:" + key, OK: true}
				for _, uuid := range p.profiles[key] {
					if !available[uuid] {
						check.OK = false
						check.Code = "squad_not_found"
						r.Ready = false
					}
				}
				r.Checks = append(r.Checks, check)
			}
		}
		var response json.RawMessage
		add("users:by-username", p.do(ctx, http.MethodGet, "/api/users/by-username/quota_readonly_scope_probe", nil, http.StatusOK, &response), true)
	}
	for _, probe := range []struct{ name, path string }{
		{"users:by-id", "/api/users/2147483647"},
		{"subscriptions:connection-keys", "/api/subscriptions/connection-keys/2147483647"},
	} {
		var response json.RawMessage
		add(probe.name, p.do(ctx, http.MethodGet, probe.path, nil, http.StatusOK, &response), true)
	}
	return r
}
