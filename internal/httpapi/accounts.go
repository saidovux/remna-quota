package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/bundle"
)

func (h *handler) capabilities(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	providers := []map[string]string{}
	if h.aggregation != nil {
		for _, id := range h.aggregation.Providers() {
			providers = append(providers, map[string]string{"id": id, "reference_type": "provider_subscription_id", "access": "read_only"})
		}
	}
	managed := []bundle.ProviderInfo{}
	if directory, ok := h.service.(managedDirectory); ok {
		managed = directory.ProviderCatalog()
	}
	writeJSON(w, 200, map[string]any{"api_version": "1", "formats": []string{"base64", "plain", "json"}, "max_sources": aggregate.MaxSources, "max_parts": 8, "providers": providers, "managed_providers": managed, "managed_accounts_enabled": h.service != nil})
}

type accountCursor struct{ After, Search, ExternalRef, Enabled string }

func (h *handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	directory, ok := h.service.(managedDirectory)
	if !ok {
		writeError(w, 404, "not_found")
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
	opts := bundle.ListOptions{Limit: limit + 1, Search: r.URL.Query().Get("search"), ExternalRef: r.URL.Query().Get("external_ref")}
	enabled := r.URL.Query().Get("enabled")
	if enabled != "" {
		if enabled != "true" && enabled != "false" {
			writeError(w, 400, "invalid_enabled")
			return
		}
		value := enabled == "true"
		opts.Enabled = &value
	}
	if token := r.URL.Query().Get("cursor"); token != "" {
		var cursor accountCursor
		data, err := base64.RawURLEncoding.DecodeString(token)
		if len(token) > 2048 || err != nil || json.Unmarshal(data, &cursor) != nil || cursor.After == "" || cursor.Search != opts.Search || cursor.ExternalRef != opts.ExternalRef || cursor.Enabled != enabled {
			writeError(w, 400, "invalid_cursor")
			return
		}
		opts.After = cursor.After
	}
	rows, err := directory.List(r.Context(), opts)
	if err != nil {
		serviceError(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		data, _ := json.Marshal(accountCursor{After: rows[len(rows)-1].ID, Search: opts.Search, ExternalRef: opts.ExternalRef, Enabled: enabled})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	items := []accountResponse{}
	for _, row := range rows {
		items = append(items, h.accountResponse(row))
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
