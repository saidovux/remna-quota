package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func TestRetryAfterSecondsAndDate(t *testing.T) {
	for _, status := range []int{429, 503} {
		for _, value := range []string{"120", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)} {
			t.Run(strconv.Itoa(status)+value, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Retry-After", value)
					w.WriteHeader(status)
				}))
				defer server.Close()
				p, err := NewRemnawave(RemnaConfig{BaseURL: server.URL, APIToken: "secret", Profiles: map[string][]string{"cdn": {testSquad}}, AllowHTTP: true})
				if err != nil {
					t.Fatal(err)
				}
				_, err = p.Read(context.Background(), "42")
				var retry *bundle.RetryError
				if !errors.As(err, &retry) || time.Until(retry.At) < 115*time.Second || time.Until(retry.At) > 121*time.Second || retry.RateLimited != (status == 429) {
					t.Fatal("Retry-After was not honored", err)
				}
			})
		}
	}
}

func TestObservationFenceStopsFurtherPanelMutations(t *testing.T) {
	stub := &panelStub{}
	p := panelProvider(t, stub)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote, part.LimitBytes = &r, 200
	before := stub.patches
	_, err = p.EnsureObserved(context.Background(), "owner", part, func(bundle.Remote) error { return bundle.ErrRevision })
	if !errors.Is(err, bundle.ErrRevision) || stub.patches != before {
		t.Fatal("observer fence did not stop obsolete mutation", err)
	}
}
