package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
)

type sourceInput struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Reference string `json:"reference"`
	Enabled   *bool  `json:"enabled"`
}

func (p sourceInput) model() aggregate.SourceInput {
	return aggregate.SourceInput{Key: p.Key, Label: p.Label, Provider: p.Provider, Reference: p.Reference, Enabled: *p.Enabled}
}

type aggregateInput struct {
	Name             string            `json:"name"`
	ExternalRef      string            `json:"external_ref"`
	Metadata         map[string]string `json:"metadata"`
	Enabled          *bool             `json:"enabled"`
	Sources          *[]sourceInput    `json:"sources"`
	ExpectedRevision *int64            `json:"expected_revision"`
}

type aggregateResponse struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	ExternalRef     string            `json:"external_ref"`
	Metadata        map[string]string `json:"metadata"`
	Enabled         bool              `json:"enabled"`
	SubscriptionURL string            `json:"subscription_url"`
	Sources         []sourceResponse  `json:"sources"`
	Status          string            `json:"status"`
	Stale           bool              `json:"stale"`
	Revision        int64             `json:"revision"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}
type sourceResponse struct {
	aggregate.SourceInput
	Username       string     `json:"username,omitempty"`
	Status         string     `json:"status"`
	UsedBytes      *int64     `json:"used_bytes"`
	LimitBytes     *int64     `json:"limit_bytes"`
	Unlimited      *bool      `json:"unlimited"`
	RemainingBytes *int64     `json:"remaining_bytes"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Stale          bool       `json:"stale"`
	CheckedAt      *time.Time `json:"checked_at"`
	LastError      string     `json:"last_error,omitempty"`
}

func (h *handler) aggregateResponse(b aggregate.Bundle) aggregateResponse {
	r := aggregateResponse{ID: b.ID, Name: b.Name, ExternalRef: b.ExternalRef, Metadata: b.Metadata, Enabled: b.Enabled, SubscriptionURL: h.publicURL + "/sub/" + url.PathEscape(b.Token), Sources: []sourceResponse{}, Status: "ready", Revision: b.Revision, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
	for _, p := range b.Sources {
		v := sourceResponse{SourceInput: p.SourceInput, Status: "pending", CheckedAt: p.CheckedAt, LastError: p.LastError, Stale: p.Snapshot == nil || p.CheckedAt == nil || p.LastError != ""}
		if p.CheckedAt != nil && time.Since(*p.CheckedAt) > h.staleAfter {
			v.Stale = true
		}
		if p.Snapshot != nil {
			snap := p.Snapshot
			used, limit, expires, unlimited := snap.UsedBytes, snap.LimitBytes, snap.ExpiresAt, snap.LimitBytes == 0
			v.Username, v.Status, v.UsedBytes, v.LimitBytes, v.Unlimited, v.ExpiresAt = snap.Username, strings.ToLower(snap.Status), &used, &limit, &unlimited, &expires
			if !unlimited {
				remaining := max(int64(0), limit-used)
				v.RemainingBytes = &remaining
			}
			if v.Status == "active" && !expires.After(time.Now()) {
				v.Status = "expired"
			} else if v.Status == "active" && !unlimited && used >= limit {
				v.Status = "limited"
			}
		}
		if !b.Enabled || !p.Enabled {
			v.Status = "excluded"
		}
		if p.Enabled && b.Enabled {
			if v.Stale {
				r.Stale = true
			}
			if p.LastError != "" {
				r.Status = "error"
			} else if p.Snapshot == nil && r.Status != "error" {
				r.Status = "pending"
			}
		}
		r.Sources = append(r.Sources, v)
	}
	if !b.Enabled {
		r.Status = "disabled"
	}
	return r
}

func decodeInput(w http.ResponseWriter, r *http.Request, dst any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		bodyError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		bodyError(w, err)
		return false
	}
	return true
}

func revisionQuery(w http.ResponseWriter, r *http.Request) (*int64, bool) {
	values, exists := r.URL.Query()["expected_revision"]
	if !exists {
		return nil, true
	}
	if len(values) != 1 {
		writeError(w, 400, "invalid_revision")
		return nil, false
	}
	v, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil || v < 0 {
		writeError(w, 400, "invalid_revision")
		return nil, false
	}
	return &v, true
}

func (h *handler) bundles(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/bundles" {
		h.listBundles(w, r)
		return
	}
	if r.URL.Path == "/api/v1/bundles/resolve" {
		h.resolveBundle(w, r)
		return
	}
	segments := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/bundles/"), "/")
	if len(segments) == 0 || segments[0] == "" || len(segments) > 3 {
		writeError(w, 404, "not_found")
		return
	}
	id := segments[0]
	var b aggregate.Bundle
	var err error
	if len(segments) == 3 && segments[1] == "sources" && segments[2] != "" {
		switch r.Method {
		case http.MethodPut:
			var input struct {
				sourceInput
				ExpectedRevision *int64 `json:"expected_revision"`
			}
			if !decodeInput(w, r, &input) {
				return
			}
			if input.Enabled == nil || (input.Key != "" && input.Key != segments[2]) {
				writeError(w, 400, "invalid_request")
				return
			}
			input.Key = segments[2]
			b, err = h.aggregation.PutSource(r.Context(), id, input.model(), input.ExpectedRevision)
		case http.MethodDelete:
			expected, ok := revisionQuery(w, r)
			if !ok {
				return
			}
			b, err = h.aggregation.RemoveSource(r.Context(), id, segments[2], expected)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			writeError(w, 405, "method_not_allowed")
			return
		}
	} else if len(segments) == 2 && (segments[1] == "refresh" || segments[1] == "rotate-token") {
		if !allowMethod(w, r, http.MethodPost) {
			return
		}
		if segments[1] == "refresh" {
			b, err = h.aggregation.Get(r.Context(), id, true)
		} else {
			expected, ok := revisionQuery(w, r)
			if !ok {
				return
			}
			b, err = h.aggregation.RotateToken(r.Context(), id, expected)
		}
	} else if len(segments) == 1 {
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			refresh := r.URL.Query().Get("refresh")
			if refresh != "" && refresh != "true" && refresh != "false" {
				writeError(w, 400, "invalid_refresh")
				return
			}
			b, err = h.aggregation.Get(r.Context(), id, refresh == "true")
		case http.MethodPut:
			var input aggregateInput
			if !decodeInput(w, r, &input) {
				return
			}
			if input.Enabled == nil || input.Sources == nil {
				writeError(w, 400, "invalid_request")
				return
			}
			wanted := aggregate.PutRequest{Name: input.Name, ExternalRef: input.ExternalRef, Metadata: input.Metadata, Enabled: *input.Enabled, ExpectedRevision: input.ExpectedRevision, Sources: []aggregate.SourceInput{}}
			for _, source := range *input.Sources {
				if source.Enabled == nil {
					writeError(w, 400, "invalid_request")
					return
				}
				wanted.Sources = append(wanted.Sources, source.model())
			}
			b, err = h.aggregation.Put(r.Context(), id, wanted)
		case http.MethodPatch:
			var input aggregate.PatchRequest
			if !decodeInput(w, r, &input) {
				return
			}
			if input.Name == nil && input.ExternalRef == nil && input.Metadata == nil && input.Enabled == nil {
				writeError(w, 400, "invalid_request")
				return
			}
			b, err = h.aggregation.Patch(r.Context(), id, input)
		case http.MethodDelete:
			expected, ok := revisionQuery(w, r)
			if !ok {
				return
			}
			if err := h.aggregation.Delete(r.Context(), id, expected); err != nil {
				aggregateError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT, PATCH, DELETE")
			writeError(w, 405, "method_not_allowed")
			return
		}
	} else {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		aggregateError(w, err)
		return
	}
	writeJSON(w, 200, h.aggregateResponse(b))
}

type bundleCursor struct {
	After       string `json:"after"`
	ExternalRef string `json:"external_ref"`
}

func (h *handler) listBundles(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, 400, "invalid_limit")
			return
		}
		limit = n
	}
	opts := aggregate.ListOptions{Limit: limit + 1, ExternalRef: r.URL.Query().Get("external_ref")}
	if token := r.URL.Query().Get("cursor"); token != "" {
		var cursor bundleCursor
		data, err := base64.RawURLEncoding.DecodeString(token)
		if len(token) > 2048 || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.After == "" || cursor.ExternalRef != opts.ExternalRef {
			writeError(w, 400, "invalid_cursor")
			return
		}
		opts.After = cursor.After
	}
	rows, err := h.aggregation.List(r.Context(), opts)
	if err != nil {
		aggregateError(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		data, _ := json.Marshal(bundleCursor{After: rows[len(rows)-1].ID, ExternalRef: opts.ExternalRef})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	items := []aggregateResponse{}
	for _, row := range rows {
		items = append(items, h.aggregateResponse(row))
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (h *handler) resolveBundle(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	var input struct {
		URL string `json:"subscription_url"`
	}
	if !decodeInput(w, r, &input) {
		return
	}
	token, ok := h.subscriptionToken(input.URL)
	if !ok {
		writeError(w, 400, "invalid_subscription_url")
		return
	}
	b, err := h.aggregation.ByToken(r.Context(), token)
	if err != nil {
		aggregateError(w, err)
		return
	}
	writeJSON(w, 200, h.aggregateResponse(b))
}

func aggregateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, aggregate.ErrNotFound):
		writeError(w, 404, "not_found")
	case errors.Is(err, aggregate.ErrInvalid):
		writeError(w, 422, "invalid_bundle")
	case errors.Is(err, aggregate.ErrRevision):
		writeError(w, 409, "revision_conflict")
	case errors.Is(err, aggregate.ErrUnavailable):
		writeError(w, 503, "source_unavailable")
	default:
		writeError(w, 500, "internal_error")
	}
}
