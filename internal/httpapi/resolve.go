package httpapi

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// resolveSubscription supports a website's existing "sign in by subscription
// link" flow. It validates the configured public origin and queries local state;
// it never fetches a caller-supplied URL or reveals a provider's private link.
func (h *handler) resolveSubscription(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "json_required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var input struct {
		SubscriptionURL string `json:"subscription_url"`
	}
	if err := decoder.Decode(&input); err != nil {
		bodyError(w, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		bodyError(w, err)
		return
	}
	token, ok := h.subscriptionToken(input.SubscriptionURL)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_subscription_url")
		return
	}
	b, err := h.service.ByToken(r.Context(), token)
	if err != nil {
		serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.accountResponse(b))
}

func (h *handler) subscriptionToken(raw string) (string, bool) {
	public, _ := url.Parse(h.publicURL)
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !strings.EqualFold(u.Scheme, public.Scheme) || !strings.EqualFold(u.Host, public.Host) || u.EscapedPath() != u.Path {
		return "", false
	}
	prefix := strings.TrimRight(public.Path, "/") + "/sub/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(u.Path, prefix)
	return token, token != "" && !strings.Contains(token, "/")
}
