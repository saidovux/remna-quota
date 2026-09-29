package bundle

import (
	"errors"
	"testing"
	"time"
)

func number(n int64) *int64 { return &n }

func TestLifetimeCounterDuplicateResetRegressionAndLegacyAnchor(t *testing.T) {
	at := time.Now().UTC()
	r := Remote{ID: "42", UsedBytes: 40, LimitBytes: 100, LifetimeUsedBytes: number(140)}
	p := Part{PartRequest: PartRequest{LimitBytes: 100, ResetStrategy: "NO_RESET"}, Remote: &r}
	if err := observe(&p, r, true); err != nil {
		t.Fatal(err)
	}
	if err := observe(&p, r, false); err != nil || UsedBytes(p) != 40 {
		t.Fatal("duplicate snapshot charged twice", err)
	}
	// Sixty additional bytes were used between observations, but only ten
	// remain in the resettable counter after a reset.
	r.UsedBytes, r.LifetimeUsedBytes, r.LastTrafficResetAt = 10, number(200), &at
	if err := observe(&p, r, false); err != nil || UsedBytes(p) != 100 || p.Traffic.CounterResets != 1 {
		t.Fatal("lifetime delta across reset lost traffic", err)
	}
	for _, bad := range []Remote{
		{ID: "42", UsedBytes: 9, LimitBytes: 100, LifetimeUsedBytes: number(199), LastTrafficResetAt: &at},
		{ID: "42", UsedBytes: 10, LimitBytes: 100, LifetimeUsedBytes: number(200)},
		{ID: "42", UsedBytes: 11, LimitBytes: 100, LifetimeUsedBytes: number(200), LastTrafficResetAt: &at},
	} {
		if err := observe(&p, bad, false); !errors.Is(err, ErrAccounting) || UsedBytes(p) != 100 || p.Remote.UsedBytes != 10 || p.AccountingStatus != "anomaly" {
			t.Fatal("inconsistent snapshot overwrote accounting", err)
		}
	}
	if err := observe(&p, r, false); err != nil || p.AccountingStatus != "ok" {
		t.Fatal("valid counter did not recover anomaly", err)
	}
	r = Remote{ID: "43", UsedBytes: 5, LimitBytes: 100, LifetimeUsedBytes: number(12)}
	if err := observe(&p, r, false); err != nil || UsedBytes(p) != 112 {
		t.Fatal("recreated user replenished quota", err)
	}
	legacy := Part{PartRequest: PartRequest{ResetStrategy: "NO_RESET"}, Traffic: &TrafficState{UsedBytes: 70, ObservedTotalBytes: 80, RemoteID: "42", RemoteUsedBytes: 20, AppliedLimitBytes: 100}}
	state, err := ProjectTraffic(legacy, Remote{ID: "42", UsedBytes: 25, LifetimeUsedBytes: number(999)})
	if err != nil || state.UsedBytes != 75 || state.ObservedTotalBytes != 85 || state.AppliedLimitBytes != 100 {
		t.Fatal("first lifetime anchor changed legacy charges", err)
	}
}

func TestFirstObservationAfterUncertainCreateIncludesEarlierResetUsage(t *testing.T) {
	for _, strategy := range []string{"NO_RESET", "MONTH"} {
		p := Part{PartRequest: PartRequest{ResetStrategy: strategy}}
		state, err := ProjectTraffic(p, Remote{ID: "42", UsedBytes: 5, LifetimeUsedBytes: number(70)})
		want := int64(70)
		if strategy == "MONTH" {
			want = 5
		}
		if err != nil || state.UsedBytes != want || state.ObservedTotalBytes != 70 {
			t.Fatal("first observation lost usage preceding native reset", err)
		}
	}
}
