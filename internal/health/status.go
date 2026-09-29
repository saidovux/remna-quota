package health

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
)

type Status struct {
	mu      sync.RWMutex
	checks  map[string]bool
	details map[string]string
}

func New() *Status { return &Status{checks: map[string]bool{}, details: map[string]string{}} }
func (s *Status) Set(name string, ok bool, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks[name] = ok
	if detail != "" {
		s.details[name] = detail
	} else {
		delete(s.details, name)
	}
}
func (s *Status) Ready() (bool, []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready := true
	var reasons []string
	for name, ok := range s.checks {
		if !ok {
			ready = false
			reason := name
			if d := s.details[name]; d != "" {
				reason += "=" + d
			}
			reasons = append(reasons, reason)
		}
	}
	sort.Strings(reasons)
	return ready, reasons
}
func (s *Status) HealthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "alive"})
}
func (s *Status) ReadyHandler(w http.ResponseWriter, _ *http.Request) {
	ready, reasons := s.Ready()
	code := http.StatusServiceUnavailable
	state := "not_ready"
	if ready {
		code = http.StatusOK
		state = "ready"
	}
	writeJSON(w, code, map[string]any{"status": state, "reasons": reasons})
}
func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
