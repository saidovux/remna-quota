// Package httpapi exposes independent subscription bundles to trusted applications
// and serves portable subscription URI lists to VPN clients.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/subscription"
)

type Service interface {
	Put(context.Context, string, bundle.PutRequest) (bundle.Bundle, error)
	Get(context.Context, string, bool) (bundle.Bundle, error)
	ByToken(context.Context, string) (bundle.Bundle, error)
	RotateToken(context.Context, string) (bundle.Bundle, error)
	Sync(context.Context, string) (bundle.Bundle, error)
	Links(context.Context, bundle.Bundle) ([]string, error)
	ConfigForJSON(context.Context, bundle.Bundle) ([]byte, error)
}

type HealthChecker interface{ Ping(context.Context) error }

type managedDirectory interface {
	List(context.Context, bundle.ListOptions) ([]bundle.Bundle, error)
	Patch(context.Context, string, bundle.PatchRequest) (bundle.Bundle, error)
	PutPart(context.Context, string, bundle.PartRequest, *int64) (bundle.Bundle, error)
	ProviderCatalog() []bundle.ProviderInfo
}

type Config struct {
	Aggregation      *aggregate.Service
	APIKey           string
	PublicURL        string
	StaleAfter       time.Duration
	BrandName        string
	BrandDescription string
	BrandAnnounce    string
	BrandHomeURL     string
	SupportURL       string
}

type handler struct {
	aggregation      *aggregate.Service
	service          Service
	health           HealthChecker
	keyHash          [32]byte
	publicURL        string
	staleAfter       time.Duration
	brandName        string
	brandDescription string
	brandAnnounce    string
	brandHomeURL     string
	supportURL       string
}

func New(service Service, health HealthChecker, config Config) (http.Handler, error) {
	if (service == nil && config.Aggregation == nil) || health == nil {
		return nil, errors.New("service and health checker are required")
	}
	if len(config.APIKey) < 32 || strings.TrimSpace(config.APIKey) != config.APIKey {
		return nil, errors.New("API key must contain at least 32 bytes without surrounding whitespace")
	}
	u, err := url.Parse(config.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("public URL must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	if config.StaleAfter <= 0 {
		config.StaleAfter = 2 * time.Minute
	}
	brandName := strings.TrimSpace(config.BrandName)
	if brandName == "" {
		brandName = "Subscription"
	}
	h := &handler{aggregation: config.Aggregation, service: service, health: health, keyHash: sha256.Sum256([]byte(config.APIKey)), publicURL: strings.TrimRight(config.PublicURL, "/"), staleAfter: config.StaleAfter, brandName: brandName, brandDescription: strings.TrimSpace(config.BrandDescription), brandAnnounce: strings.TrimSpace(config.BrandAnnounce), brandHomeURL: strings.TrimRight(config.BrandHomeURL, "/"), supportURL: strings.TrimSpace(config.SupportURL)}
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Method == http.MethodHead {
		w = headWriter{w}
	}
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		if r.URL.Path == "/readyz" {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if h.health.Ping(ctx) != nil {
				writeError(w, http.StatusServiceUnavailable, "not_ready")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !h.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if r.URL.Path == "/api/v1/capabilities" {
			h.capabilities(w, r)
			return
		}
		if r.URL.Path == "/api/v1/diagnostics" || r.URL.Path == "/api/v1/metrics" {
			h.diagnostics(w, r)
			return
		}
		if h.aggregation != nil && (r.URL.Path == "/api/v1/bundles" || strings.HasPrefix(r.URL.Path, "/api/v1/bundles/")) {
			h.bundles(w, r)
			return
		}
		if h.service == nil {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if r.URL.Path == "/api/v1/subscriptions/resolve" {
			h.resolveSubscription(w, r)
			return
		}
		h.account(w, r)
		return
	}
	if r.URL.Path == "/sub-assets/qrcode.js" {
		h.subpageAsset(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/sub/") {
		h.subscription(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found")
}

func (h *handler) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, key, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || key == "" {
		return false
	}
	actual := sha256.Sum256([]byte(key))
	return subtle.ConstantTimeCompare(actual[:], h.keyHash[:]) == 1
}

func (h *handler) account(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/accounts" {
		h.listAccounts(w, r)
		return
	}
	const prefix = "/api/v1/accounts/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	segments := strings.Split(path, "/")
	if len(segments) > 3 || segments[0] == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	id := segments[0]
	if len(segments) >= 2 && segments[1] == "devices" {
		h.devices(w, r, id, segments[2:])
		return
	}
	var b bundle.Bundle
	var err error
	if len(segments) == 3 {
		directory, ok := h.service.(managedDirectory)
		if !ok || segments[1] != "parts" || segments[2] == "" {
			writeError(w, 404, "not_found")
			return
		}
		if !allowMethod(w, r, http.MethodPut) {
			return
		}
		var input struct {
			partInput
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if !decodeInput(w, r, &input) {
			return
		}
		if input.Enabled == nil || input.LimitBytes == nil || (input.Key != "" && input.Key != segments[2]) {
			writeError(w, 400, "invalid_request")
			return
		}
		b, err = directory.PutPart(r.Context(), id, bundle.PartRequest{Key: segments[2], Label: input.Label, Provider: input.Provider, Profile: input.Profile, LimitBytes: *input.LimitBytes, ExpiresAt: input.ExpiresAt, Enabled: *input.Enabled, ResetStrategy: input.ResetStrategy}, input.ExpectedRevision)
	} else if len(segments) == 2 {
		switch segments[1] {
		case "sync":
			if !allowMethod(w, r, http.MethodPost) {
				return
			}
			b, err = h.service.Sync(r.Context(), id)
		case "rotate-token":
			if !allowMethod(w, r, http.MethodPost) {
				return
			}
			b, err = h.service.RotateToken(r.Context(), id)
		case "balance", "autorenew", "periods":
			b, err = h.control(w, r, id, segments[1])
		default:
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
	} else {
		switch r.Method {
		case http.MethodPatch:
			directory, ok := h.service.(managedDirectory)
			if !ok {
				writeError(w, 405, "method_not_allowed")
				return
			}
			var input bundle.PatchRequest
			if !decodeInput(w, r, &input) {
				return
			}
			if input.Name == nil && input.ExternalRef == nil && input.Enabled == nil && input.DeviceLimit == nil {
				writeError(w, 400, "invalid_request")
				return
			}
			b, err = directory.Patch(r.Context(), id, input)
		case http.MethodGet, http.MethodHead:
			refresh := r.URL.Query().Get("refresh")
			if refresh != "" && refresh != "true" && refresh != "false" {
				writeError(w, http.StatusBadRequest, "invalid_refresh")
				return
			}
			b, err = h.service.Get(r.Context(), id, refresh == "true")
		case http.MethodPut:
			contentType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if parseErr != nil || contentType != "application/json" {
				writeError(w, http.StatusUnsupportedMediaType, "json_required")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			var input putInput
			if decodeErr := decoder.Decode(&input); decodeErr != nil {
				bodyError(w, decodeErr)
				return
			}
			var trailing any
			if decodeErr := decoder.Decode(&trailing); decodeErr != io.EOF {
				bodyError(w, decodeErr)
				return
			}
			request, valid := input.request()
			if !valid {
				writeError(w, http.StatusBadRequest, "invalid_request")
				return
			}
			b, err = h.service.Put(r.Context(), id, request)
		case http.MethodDelete:
			lifecycle, ok := h.service.(lifecycleService)
			if !ok {
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
				return
			}
			if deleteErr := lifecycle.DeleteAccount(r.Context(), id); deleteErr != nil {
				serviceError(w, deleteErr)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		default:
			w.Header().Set("Allow", "GET, HEAD, PUT, PATCH, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
	}
	if errors.Is(err, errResponseWritten) {
		return
	}
	if err != nil {
		serviceError(w, err)
		return
	}
	response := h.accountResponse(b)
	status := http.StatusOK
	if (r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodPatch) && response.SyncStatus != "ready" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, response)
}

func (h *handler) subscription(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/sub/")
	if token == "" || strings.Contains(token, "/") || len(token) > 256 {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	// A real browser navigation always gets the human-readable page, even with
	// ?format=... (browsers cannot provide HWID for the config path). VPN clients
	// are detected and keep receiving the configuration.
	if wantsSubscriptionPage(r) {
		h.subscriptionPage(w, r, token)
		return
	}
	format := r.URL.Query().Get("format")
	inferredJSON := false
	if format == "" {
		if wantsJSONSubscription(r) {
			format, inferredJSON = "json", true
		} else {
			format = "base64"
		}
	}
	if format != "base64" && format != "plain" && format != "json" {
		writeError(w, http.StatusBadRequest, "unsupported_format")
		return
	}
	if h.aggregation != nil {
		b, err := h.aggregation.ByToken(r.Context(), token)
		if err == nil {
			links, err := h.aggregation.Links(r.Context(), b)
			if err != nil {
				aggregateError(w, err)
				return
			}
			h.applyAggregateHeaders(w, token, b)
			if format == "json" {
				h.writeSubscriptionJSON(w, bundle.Bundle{}, links)
				return
			}
			h.writeSubscription(w, links, format)
			return
		}
		if !errors.Is(err, aggregate.ErrNotFound) {
			aggregateError(w, err)
			return
		}
	}
	if h.service == nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	b, err := h.service.ByToken(r.Context(), token)
	if err != nil {
		serviceError(w, err)
		return
	}
	h.applySubscriptionHeaders(w, token, h.accountResponse(b).Parts)
	if format == "json" {
		if h.writeJSONConfig(w, r, b, inferredJSON) {
			return
		}
		// A client that prefers JSON but cannot use it right now (missing HWID,
		// provider without JSON support) still receives the link list.
		format = "base64"
	}
	var links []string
	if deviceService, ok := h.service.(deviceService); ok {
		if bundle.DeviceLimit(b) > 0 {
			w.Header().Set("x-hwid-active", "true")
		}
		device := bundle.Device{HWID: r.Header.Get("x-hwid"), Platform: r.Header.Get("x-device-os"), OSVersion: r.Header.Get("x-ver-os"), Model: r.Header.Get("x-device-model"), UserAgent: r.UserAgent()}
		links, err = deviceService.LinksForDevice(r.Context(), b, device)
	} else if bundle.DeviceLimit(b) > 0 {
		err = bundle.ErrUnavailable
	} else {
		links, err = h.service.Links(r.Context(), b)
	}
	if err != nil {
		if errors.Is(err, bundle.ErrHWIDRequired) {
			w.Header().Set("x-hwid-not-supported", "true")
			writeError(w, 403, "hwid_required")
			return
		}
		if errors.Is(err, bundle.ErrDeviceLimit) {
			w.Header().Set("x-hwid-max-devices-reached", "true")
			w.Header().Set("x-hwid-limit", "true")
			writeError(w, 403, "device_limit_reached")
			return
		}
		serviceError(w, err)
		return
	}
	h.writeSubscription(w, links, format)
}

// writeJSONConfig serves the merged Xray JSON configuration. It returns false
// when the caller should fall back to the link list (only for clients that were
// auto-detected as JSON-capable).
func (h *handler) writeJSONConfig(w http.ResponseWriter, r *http.Request, b bundle.Bundle, fallback bool) bool {
	if bundle.DeviceLimit(b) > 0 {
		w.Header().Set("x-hwid-active", "true")
	}
	device := bundle.Device{HWID: r.Header.Get("x-hwid"), Platform: r.Header.Get("x-device-os"), OSVersion: r.Header.Get("x-ver-os"), Model: r.Header.Get("x-device-model"), UserAgent: r.UserAgent()}
	var config []byte
	var err error
	if deviceJSON, ok := h.service.(configDeviceService); ok {
		config, err = deviceJSON.ConfigForJSONForDevice(r.Context(), b, device)
	} else if bundle.DeviceLimit(b) > 0 {
		err = bundle.ErrUnavailable
	} else {
		config, err = h.service.ConfigForJSON(r.Context(), b)
	}
	if err == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(config)
		return true
	}
	if fallback {
		return false
	}
	switch {
	case errors.Is(err, bundle.ErrHWIDRequired):
		w.Header().Set("x-hwid-not-supported", "true")
		writeError(w, http.StatusForbidden, "hwid_required")
	case errors.Is(err, bundle.ErrDeviceLimit):
		w.Header().Set("x-hwid-max-devices-reached", "true")
		w.Header().Set("x-hwid-limit", "true")
		writeError(w, http.StatusForbidden, "device_limit_reached")
	default:
		serviceError(w, err)
	}
	return true
}

func (h *handler) writeSubscription(w http.ResponseWriter, links []string, format string) {
	data, err := subscription.Encode(links, format)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=subscription.txt")
	// A mixed unlimited/capped bundle has no meaningful combined traffic total.
	// Deliberately do not emit subscription-userinfo with misleading sums.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	if method == http.MethodGet && r.Method == http.MethodHead {
		return true
	}
	if method == http.MethodGet {
		method = "GET, HEAD"
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	return false
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(data []byte) (int, error) { return len(data), nil }

func bodyError(w http.ResponseWriter, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_json")
}

func serviceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bundle.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, bundle.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_account")
	case errors.Is(err, bundle.ErrConflict):
		writeError(w, http.StatusConflict, "account_conflict")
	case errors.Is(err, bundle.ErrRevision):
		writeError(w, http.StatusConflict, "revision_conflict")
	case errors.Is(err, bundle.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "provider_unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
