package bundle

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestEnforcementDoesNotRaiseExhaustedNativeLimit(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		charged, nativeUsed, limit, want int64
	}{
		{"overrun without reset", 76944860, 76944860, 50000000, 50000000},
		{"overrun after reset", 90, 60, 70, 40},
		{"exhausted after reset", 90, 0, 70, 1},
		{"exhausted new user", 90, 3, 70, 1},
		{"large counters", math.MaxInt64, math.MaxInt64, 70, 70},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Part{PartRequest: PartRequest{Enabled: true, ResetStrategy: "NO_RESET", LimitBytes: tc.limit}, Traffic: &TrafficState{RemoteID: "42", UsedBytes: tc.charged, RemoteUsedBytes: tc.nativeUsed}}
			r := Remote{ID: "42", UsedBytes: tc.nativeUsed, LimitBytes: tc.nativeUsed}
			for range 3 {
				effective, err := EnforcementPart(p, r)
				if err != nil || effective.Enabled || effective.LimitBytes != tc.want {
					t.Fatalf("cap changed after overrun: enabled=%v cap=%d want=%d err=%v", effective.Enabled, effective.LimitBytes, tc.want, err)
				}
				r.LimitBytes = effective.LimitBytes
			}
		})
	}
}

func TestCounterResetPreservesPackageAndCDNExhaustionKeepsMain(t *testing.T) {
	s, p, req := setupService(t)
	const gib = int64(1 << 30)
	req.Parts[0].LimitBytes = 200 * gib
	req.Parts[1].LimitBytes = 70 * gib
	b := mustPut(t, s, req)
	setUsage := func(key string, used int64, reset bool) {
		r := p.users["account-1/"+key]
		r.UsedBytes = used
		if reset {
			at := s.now()
			if r.LastTrafficResetAt != nil {
				at = r.LastTrafficResetAt.Add(time.Second)
			}
			r.LastTrafficResetAt = &at
		}
		p.users[r.ID] = r
	}
	setUsage("main", 90*gib, false)
	setUsage("cdn", 37*gib, false)
	b, err := s.Sync(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	setUsage("cdn", 5*gib, true)
	b, err = s.Sync(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	cdn := b.Parts[1]
	if UsedBytes(cdn) != 42*gib || cdn.Remote.LimitBytes != 33*gib || cdn.Traffic.CounterResets != 1 || cdn.Traffic.AppliedLimitBytes != 70*gib {
		t.Fatalf("reset replenished package: %+v", cdn.Traffic)
	}
	// Repeated reads and reconciliation must not double count the same snapshot.
	for range 3 {
		b, err = s.Get(context.Background(), b.ID, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	if UsedBytes(b.Parts[1]) != 42*gib {
		t.Fatal("reads double counted usage")
	}
	setUsage("cdn", 33*gib, false)
	b, err = s.Sync(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if UsedBytes(b.Parts[1]) != 70*gib || b.Parts[1].Remote.Status != "DISABLED" || b.Parts[0].Remote.Status != "ACTIVE" || UsedBytes(b.Parts[0]) != 90*gib {
		t.Fatal("independent enforcement failed")
	}
	links, err := s.Links(context.Background(), b)
	if err != nil || len(links) != 1 {
		t.Fatalf("MAIN unavailable after CDN exhaustion: %v", err)
	}
	// A reset of an exhausted user must never translate to the native unlimited zero.
	setUsage("cdn", 0, true)
	b, err = s.Sync(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Parts[1].Remote.Status != "DISABLED" || b.Parts[1].Remote.LimitBytes != 1 || UsedBytes(b.Parts[1]) != 70*gib {
		t.Fatal("reset reenabled exhausted CDN")
	}
	// Granting a new 30 GiB is an absolute increase to 100 GiB, preserving history.
	req.Parts[1].LimitBytes = 100 * gib
	b = mustPut(t, s, req)
	if b.Parts[1].Remote.Status != "ACTIVE" || b.Parts[1].Remote.LimitBytes != 30*gib || UsedBytes(b.Parts[1]) != 70*gib || b.Parts[0].Remote.LimitBytes != 200*gib {
		t.Fatal("top-up or independent main quota failed")
	}
}

func TestTrafficResetTimestampRecreationAndPeriodicCycles(t *testing.T) {
	at := time.Now().UTC()
	for _, strategy := range []string{"NO_RESET", "MONTH"} {
		part := Part{PartRequest: PartRequest{ResetStrategy: strategy, Enabled: true, LimitBytes: 70}, Remote: &Remote{ID: "old", UsedBytes: 10, LimitBytes: 70}}
		state, err := ProjectTraffic(part, Remote{ID: "old", UsedBytes: 15, LastTrafficResetAt: &at})
		if err != nil {
			t.Fatal(err)
		}
		want := int64(25)
		if strategy == "MONTH" {
			want = 15
		}
		if state.UsedBytes != want || state.ObservedTotalBytes != 25 || state.CounterResets != 1 {
			t.Fatalf("larger counter after timestamp reset lost bytes: %+v", state)
		}
		part.Traffic = &state
		state, err = ProjectTraffic(part, Remote{ID: "new", UsedBytes: 2})
		if err != nil {
			t.Fatal(err)
		}
		want = 27
		if strategy == "MONTH" {
			want = 2
		}
		if state.UsedBytes != want || state.ObservedTotalBytes != 27 || state.CounterResets != 2 {
			t.Fatalf("recreated user lost observed usage: %+v", state)
		}
	}
}
