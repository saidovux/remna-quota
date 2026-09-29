package httpapi

import (
	"context"
	"github.com/saidovux/remna-quota/internal/bundle"
	"net/http"
)

type deviceService interface {
	LinksForDevice(context.Context, bundle.Bundle, bundle.Device) ([]string, error)
	DeleteDevice(context.Context, string, string) (bundle.Bundle, error)
}

// configDeviceService builds the JSON config while enforcing the device limit.
type configDeviceService interface {
	ConfigForJSONForDevice(context.Context, bundle.Bundle, bundle.Device) ([]byte, error)
}

func (h *handler) devices(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	service, ok := h.service.(deviceService)
	if !ok {
		writeError(w, 404, "not_found")
		return
	}
	if len(tail) == 0 {
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		b, err := h.service.Get(r.Context(), id, false)
		if err != nil {
			serviceError(w, err)
			return
		}
		items := b.Devices
		if items == nil {
			items = []bundle.Device{}
		}
		writeJSON(w, 200, map[string]any{"device_limit": bundle.DeviceLimit(b), "device_count": len(items), "items": items})
		return
	}
	if len(tail) != 1 || tail[0] == "" {
		writeError(w, 404, "not_found")
		return
	}
	if !allowMethod(w, r, http.MethodDelete) {
		return
	}
	b, err := service.DeleteDevice(r.Context(), id, tail[0])
	if err != nil {
		serviceError(w, err)
		return
	}
	response := h.accountResponse(b)
	status := 200
	if response.SyncStatus != "ready" {
		status = 202
	}
	writeJSON(w, status, response)
}
