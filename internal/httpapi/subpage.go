package httpapi

import (
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
)

//go:embed assets/subpage.html assets/qrcode.js
var subpageAssets embed.FS

var subpageTemplate = template.Must(template.ParseFS(subpageAssets, "assets/subpage.html"))

type subpagePart struct {
	Label          string
	Hint           string
	StatusLabel    string
	UsedHuman      string
	LimitHuman     string
	RemainingHuman string
	ExpiresHuman   string
	Percent        int
	Unlimited      bool
}

type subpageData struct {
	BrandName        string
	BrandDescription string
	Logo             string
	PlanName         string
	StatusLabel      string
	StatusClass      string
	SubscriptionURL  string
	UsedHuman        string
	TotalHuman       string
	DaysLeft         int
	Percent          int
	HasTraffic       bool
	Parts            []subpagePart
	DeviceUsed       int
	DeviceLimit      int
	HomeURL          string
	SupportURL       string
	UsedBytes        int64
	TotalBytes       int64
}

// wantsSubscriptionPage reports whether a request looks like a browser opening
// the subscription link rather than a VPN client downloading a config. It is
// deliberately conservative: clients that send an HWID or accept non-HTML keep
// receiving the config.
func wantsSubscriptionPage(r *http.Request) bool {
	if r.Header.Get("x-hwid") != "" {
		return false
	}
	accept := r.Header.Get("Accept")
	if !strings.Contains(accept, "text/html") {
		return false
	}
	return !strings.Contains(strings.ToLower(accept), "application/json")
}

func (h *handler) subscriptionPage(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()
	if h.service != nil {
		if b, err := h.service.ByToken(ctx, token); err == nil {
			h.renderSubscriptionPage(w, h.subscriptionPageData(h.accountResponse(b), token))
			return
		}
	}
	if h.aggregation != nil {
		if b, err := h.aggregation.ByToken(ctx, token); err == nil {
			h.renderSubscriptionPage(w, h.aggregatePageData(b, token))
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found")
}

func (h *handler) renderSubscriptionPage(w http.ResponseWriter, data subpageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = subpageTemplate.Execute(w, data)
}

func (h *handler) subscriptionPageData(a accountResponse, token string) subpageData {
	data := subpageData{
		BrandName:        h.brandName,
		BrandDescription: h.brandDescription,
		Logo:             brandInitial(h.brandName),
		PlanName:         strings.TrimSpace(a.Name),
		SubscriptionURL:  h.publicURL + "/sub/" + url.PathEscape(token),
		DeviceLimit:      a.DeviceLimit,
		DeviceUsed:       a.DeviceCount,
		HomeURL:          h.brandHomeURL,
		SupportURL:       h.supportURL,
	}
	if data.PlanName == "" {
		data.PlanName = h.brandName
	}
	data.StatusLabel, data.StatusClass = statusPresentation(a.Status)
	for _, part := range a.Parts {
		if !part.Enabled {
			continue
		}
		used := int64(0)
		if part.UsedBytes != nil {
			used = *part.UsedBytes
		}
		percent := 0
		if !part.Unlimited && part.LimitBytes > 0 {
			percent = int(used * 100 / part.LimitBytes)
			if percent > 100 {
				percent = 100
			}
		}
		item := subpagePart{
			Label:       part.Label,
			Hint:        partHint(part.Key),
			Unlimited:   part.Unlimited,
			UsedHuman:   humanBytes(used),
			LimitHuman:  humanBytes(part.LimitBytes),
			Percent:     percent,
			StatusLabel: partStatusPresentation(part.Status),
		}
		if part.Unlimited {
			item.RemainingHuman = "∞"
			item.LimitHuman = "∞"
		} else {
			remaining := part.LimitBytes - used
			if remaining < 0 {
				remaining = 0
			}
			item.RemainingHuman = humanBytes(remaining)
		}
		if !part.ExpiresAt.IsZero() && part.ExpiresAt.After(time.Now()) {
			item.ExpiresHuman = part.ExpiresAt.UTC().Format("02.01.2006")
		}
		data.Parts = append(data.Parts, item)
		if !part.Unlimited {
			data.TotalBytes += part.LimitBytes
		}
		data.UsedBytes += used
		if part.DaysLeft != nil && *part.DaysLeft > data.DaysLeft {
			data.DaysLeft = *part.DaysLeft
		}
	}
	if data.TotalBytes > 0 {
		data.HasTraffic = true
		data.TotalHuman = humanBytes(data.TotalBytes)
		data.UsedHuman = humanBytes(data.UsedBytes)
		data.Percent = int(data.UsedBytes * 100 / data.TotalBytes)
		if data.Percent > 100 {
			data.Percent = 100
		}
	}
	return data
}

func (h *handler) aggregatePageData(b aggregate.Bundle, token string) subpageData {
	data := subpageData{
		BrandName:        h.brandName,
		BrandDescription: h.brandDescription,
		Logo:             brandInitial(h.brandName),
		PlanName:         strings.TrimSpace(b.Name),
		SubscriptionURL:  h.publicURL + "/sub/" + url.PathEscape(token),
		HomeURL:          h.brandHomeURL,
		SupportURL:       h.supportURL,
	}
	if data.PlanName == "" {
		data.PlanName = h.brandName
	}
	if !b.Enabled {
		data.StatusLabel, data.StatusClass = "Отключена", "expired"
	} else {
		data.StatusLabel, data.StatusClass = "Активна", ""
	}
	for _, source := range b.Sources {
		if !source.Enabled {
			continue
		}
		item := subpagePart{Label: source.Label, StatusLabel: "активен", LimitHuman: "—"}
		if item.Label == "" {
			item.Label = source.Key
		}
		if source.Snapshot != nil {
			item.UsedHuman = humanBytes(source.Snapshot.UsedBytes)
			item.LimitHuman = humanBytes(source.Snapshot.LimitBytes)
			remaining := source.Snapshot.LimitBytes - source.Snapshot.UsedBytes
			if remaining < 0 {
				remaining = 0
			}
			item.RemainingHuman = humanBytes(remaining)
			if source.Snapshot.LimitBytes > 0 {
				item.Percent = int(source.Snapshot.UsedBytes * 100 / source.Snapshot.LimitBytes)
				if item.Percent > 100 {
					item.Percent = 100
				}
			}
			days := daysLeftUntil(source.Snapshot.ExpiresAt)
			if days < 0 {
				days = 0
			}
			if days > data.DaysLeft {
				data.DaysLeft = days
			}
			data.UsedBytes += source.Snapshot.UsedBytes
			data.TotalBytes += source.Snapshot.LimitBytes
		}
		data.Parts = append(data.Parts, item)
	}
	if data.TotalBytes > 0 {
		data.HasTraffic = true
		data.TotalHuman = humanBytes(data.TotalBytes)
		data.UsedHuman = humanBytes(data.UsedBytes)
		data.Percent = int(data.UsedBytes * 100 / data.TotalBytes)
		if data.Percent > 100 {
			data.Percent = 100
		}
	}
	return data
}

// applySubscriptionHeaders advertises the profile to VPN clients so they show a
// meaningful name and traffic instead of a generic placeholder.
func (h *handler) applySubscriptionHeaders(w http.ResponseWriter, token string, parts []partResponse) {
	w.Header().Set("profile-title", encodeHeaderValue(h.brandName))
	if h.brandDescription != "" {
		w.Header().Set("profile-description", encodeHeaderValue(h.brandDescription))
	}
	w.Header().Set("profile-update-interval", "12")
	w.Header().Set("profile-web-page-url", h.publicURL+"/sub/"+url.PathEscape(token))
	if h.brandDescription != "" {
		w.Header().Set("profile-description", encodeHeaderValue(h.brandDescription))
	}
	if h.brandAnnounce != "" {
		announce := strings.ReplaceAll(h.brandAnnounce, "{days}", strconv.Itoa(maxDaysLeft(parts)))
		w.Header().Set("announce", encodeHeaderValue(announce))
	}
	if h.brandHomeURL != "" {
		w.Header().Set("announce-url", h.brandHomeURL)
	}
	if h.supportURL != "" {
		w.Header().Set("support-url", h.supportURL)
	}
	if info, ok := subscriptionUserInfo(parts); ok {
		w.Header().Set("subscription-userinfo", info)
	}
}

func maxDaysLeft(parts []partResponse) int {
	days := 0
	for _, part := range parts {
		if part.DaysLeft != nil && *part.DaysLeft > days {
			days = *part.DaysLeft
		}
	}
	return days
}

func (h *handler) applyAggregateHeaders(w http.ResponseWriter, token string, b aggregate.Bundle) {
	parts := make([]partResponse, 0, len(b.Sources))
	for _, source := range b.Sources {
		if !source.Enabled || source.Snapshot == nil {
			continue
		}
		used := source.Snapshot.UsedBytes
		parts = append(parts, partResponse{Enabled: true, LimitBytes: source.Snapshot.LimitBytes, UsedBytes: &used, ExpiresAt: source.Snapshot.ExpiresAt})
	}
	h.applySubscriptionHeaders(w, token, parts)
}

func subscriptionUserInfo(parts []partResponse) (string, bool) {
	var download, total, expire int64
	for _, part := range parts {
		if !part.Enabled {
			continue
		}
		if part.Unlimited {
			// A mixed unlimited/capped bundle has no meaningful combined total.
			return "", false
		}
		if part.UsedBytes != nil {
			download += *part.UsedBytes
		}
		total += part.LimitBytes
		if e := part.ExpiresAt.Unix(); e > expire {
			expire = e
		}
	}
	if total <= 0 {
		return "", false
	}
	return fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", download, total, expire), true
}

func encodeHeaderValue(value string) string {
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(value))
}

func brandInitial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "S"
}

func statusPresentation(status string) (string, string) {
	switch status {
	case "active":
		return "Активна", ""
	case "partial":
		return "Частично активна", "pending"
	case "disabled":
		return "Отключена", "expired"
	case "unavailable":
		return "Недоступна", "expired"
	case "":
		return "Недоступна", "expired"
	default:
		return strings.ToUpper(status), "expired"
	}
}

func partHint(key string) string {
	switch key {
	case "main":
		return "Обычный трафик (без CDN)"
	case "cdn":
		return "Трафик через CDN"
	default:
		return ""
	}
}

func partStatusPresentation(status string) string {
	switch status {
	case "active":
		return "активен"
	case "expired":
		return "срок истёк"
	case "limited":
		return "трафик исчерпан"
	case "disabled":
		return "отключён"
	case "":
		return "нет данных"
	default:
		return status
	}
}

func humanBytes(bytes int64) string {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
		tib = 1024 * gib
	)
	switch {
	case bytes >= tib:
		return fmt.Sprintf("%.2f ТиБ", float64(bytes)/tib)
	case bytes >= gib:
		return fmt.Sprintf("%.2f ГиБ", float64(bytes)/gib)
	case bytes >= mib:
		return fmt.Sprintf("%.1f МиБ", float64(bytes)/mib)
	default:
		return fmt.Sprintf("%d Б", bytes)
	}
}

// subpageAsset serves the embedded QR generator used by the subscription page.
func (h *handler) subpageAsset(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	payload, err := subpageAssets.ReadFile("assets/qrcode.js")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}
