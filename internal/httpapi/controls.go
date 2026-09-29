package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/saidovux/remna-quota/internal/bundle"
)

// lifecycleService is the optional control plane for balances, autorenew and
// prepaid periods. It is implemented by *bundle.Service.
type lifecycleService interface {
	AddBalance(context.Context, string, int64, string) (bundle.Bundle, error)
	SetAutorenew(context.Context, string, bool) (bundle.Bundle, error)
	AddPeriods(context.Context, string, int, string) (bundle.Bundle, error)
	DeleteAccount(context.Context, string) error
}

// errResponseWritten signals that the control handler already wrote a response
// and the caller must not write another.
var errResponseWritten = errors.New("response already written")

func (h *handler) control(w http.ResponseWriter, r *http.Request, id, action string) (bundle.Bundle, error) {
	if !allowMethod(w, r, http.MethodPost) {
		return bundle.Bundle{}, errResponseWritten
	}
	lifecycle, ok := h.service.(lifecycleService)
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return bundle.Bundle{}, errResponseWritten
	}
	switch action {
	case "balance":
		var input bundle.BalanceDeltaRequest
		if !decodeInput(w, r, &input) {
			return bundle.Bundle{}, errResponseWritten
		}
		return lifecycle.AddBalance(r.Context(), id, input.DeltaMinor, input.IdempotencyKey)
	case "autorenew":
		var input bundle.AutorenewRequest
		if !decodeInput(w, r, &input) {
			return bundle.Bundle{}, errResponseWritten
		}
		return lifecycle.SetAutorenew(r.Context(), id, input.Enabled)
	case "periods":
		var input bundle.PeriodsRequest
		if !decodeInput(w, r, &input) {
			return bundle.Bundle{}, errResponseWritten
		}
		return lifecycle.AddPeriods(r.Context(), id, input.Count, input.IdempotencyKey)
	default:
		writeError(w, http.StatusNotFound, "not_found")
		return bundle.Bundle{}, errResponseWritten
	}
}
