package providers

import (
	"context"
	"net/http"
	"strconv"

	"github.com/saidovux/remna-quota/internal/bundle"
)

type remnaDevices struct {
	Response struct {
		Total   int `json:"total"`
		Devices []struct {
			HWID   string `json:"hwid"`
			UserID int64  `json:"userId"`
		} `json:"devices"`
	} `json:"response"`
}

// SyncDevices converges to a single account-owned registry. Never merge native
// per-part registrations: doing so would grant separate device slots per part.
func (p *Remnawave) SyncDevices(ctx context.Context, owner string, part bundle.Part, limit int, devices []bundle.Device) error {
	if part.Remote == nil || limit < 0 || limit > 1000 || (limit > 0 && len(devices) > limit) {
		return bundle.ErrInvalid
	}
	id, err := strconv.ParseInt(part.Remote.ID, 10, 64)
	if err != nil || id <= 0 || id > maxJSONInteger {
		return bundle.ErrInvalid
	}
	user, err := p.getUser(ctx, "/api/users/"+strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	if user.ID != id || user.Username != part.Username || user.Description != ownership(owner, part.Key) {
		return bundle.ErrConflict
	}
	squadMatches := func(u remnaUser) bool {
		return p.hwidExternalSquadUUID == "" || (u.ExternalSquadUUID != nil && *u.ExternalSquadUUID == p.hwidExternalSquadUUID)
	}
	if user.HWIDDeviceLimit == nil || *user.HWIDDeviceLimit != limit || !squadMatches(user) {
		body := map[string]any{"id": id, "hwidDeviceLimit": limit}
		if p.hwidExternalSquadUUID != "" {
			body["externalSquadUuid"] = p.hwidExternalSquadUUID
		}
		_, err = p.mutate(ctx, http.MethodPatch, "/api/users", body, http.StatusOK)
		if err != nil {
			return err
		}
	}
	var current remnaDevices
	path := "/api/hwid/devices/" + strconv.FormatInt(id, 10)
	if err = p.do(ctx, http.MethodGet, path, nil, http.StatusOK, &current); err != nil {
		return err
	}
	wanted := make(map[string]bundle.Device, len(devices))
	for _, d := range devices {
		wanted[d.HWID] = d
	}
	present := make(map[string]bool)
	for _, d := range current.Response.Devices {
		if d.UserID != id {
			return bundle.ErrUnavailable
		}
		if _, ok := wanted[d.HWID]; ok {
			present[d.HWID] = true
			continue
		}
		var result remnaDevices
		if err = p.do(ctx, http.MethodPost, "/api/hwid/devices/delete", map[string]any{"userId": id, "hwid": d.HWID}, http.StatusOK, &result); err != nil {
			return err
		}
	}
	// Remnawave's administrative create endpoint treats zero as a full limit.
	// Preserve the durable registry while enforcement is disabled; restore on enable.
	if limit > 0 {
		for _, d := range devices {
			if present[d.HWID] {
				continue
			}
			var result remnaDevices
			body := map[string]any{"userId": id, "hwid": d.HWID, "platform": d.Platform, "osVersion": d.OSVersion, "deviceModel": d.Model, "userAgent": d.UserAgent}
			if err = p.do(ctx, http.MethodPost, "/api/hwid/devices", body, http.StatusOK, &result); err != nil {
				return err
			}
		}
	}
	user, err = p.getUser(ctx, "/api/users/"+strconv.FormatInt(id, 10))
	if err != nil {
		return err
	}
	if user.Description != ownership(owner, part.Key) || user.Username != part.Username || user.HWIDDeviceLimit == nil || *user.HWIDDeviceLimit != limit || !squadMatches(user) {
		return bundle.ErrUnavailable
	}
	if limit > 0 {
		if err = p.do(ctx, http.MethodGet, path, nil, http.StatusOK, &current); err != nil {
			return err
		}
		if current.Response.Total != len(wanted) || len(current.Response.Devices) != len(wanted) {
			return bundle.ErrUnavailable
		}
		seen := map[string]bool{}
		for _, d := range current.Response.Devices {
			if _, ok := wanted[d.HWID]; !ok || d.UserID != id || seen[d.HWID] {
				return bundle.ErrUnavailable
			}
			seen[d.HWID] = true
		}
	}
	return nil
}

func (p *Demo) SyncDevices(ctx context.Context, owner string, part bundle.Part, limit int, devices []bundle.Device) error {
	if part.Remote == nil {
		return bundle.ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owners[part.Username] != part.Remote.ID {
		return bundle.ErrConflict
	}
	return ctx.Err()
}
