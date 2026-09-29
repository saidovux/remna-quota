package providers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func TestEnsureOverrunPreservesOriginalCapAndNeverReenables(t *testing.T) {
	for _, status := range []string{"ACTIVE", "LIMITED", "DISABLED"} {
		for _, nativeCap := range []int64{50000000, 76944860} {
			t.Run(fmt.Sprintf("%s/cap-%d", status, nativeCap), func(t *testing.T) {
				stub := &panelStub{}
				p := panelProvider(t, stub)
				part := testPart()
				part.ResetStrategy, part.LimitBytes = "NO_RESET", 50000000
				r, err := p.Ensure(context.Background(), "owner", part)
				if err != nil {
					t.Fatal(err)
				}
				part.Remote = &r
				stub.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(76944860)
				stub.user["trafficLimitBytes"], stub.user["status"] = float64(nativeCap), status
				for range 3 {
					r, err = p.Ensure(context.Background(), "owner", part)
					if err != nil {
						t.Fatal(err)
					}
					if r.LimitBytes != 50000000 || r.Status != "DISABLED" || r.UsedBytes != 76944860 || stub.enables != 0 {
						t.Fatalf("overrun changed entitlement: %+v enables=%d", r, stub.enables)
					}
					part.Remote = &r
				}
				wantPatches := 0
				if nativeCap != 50000000 {
					wantPatches = 1
				}
				if stub.patches != wantPatches {
					t.Fatalf("repeated sync rewrote cap: patches=%d", stub.patches)
				}
			})
		}
	}
}

func TestEnsureNativeBudgetAfterCounterResetAndRecreation(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		stub := &panelStub{}
		p := panelProvider(t, stub)
		part := testPart()
		part.ResetStrategy = "NO_RESET"
		part.LimitBytes = 70
		remote, err := p.Ensure(context.Background(), "owner", part)
		if err != nil {
			t.Fatal(err)
		}
		remote.UsedBytes = 37
		part.Remote = &remote
		part.Traffic = &bundle.TrafficState{UsedBytes: 37, ObservedTotalBytes: 37, RemoteID: remote.ID, RemoteUsedBytes: 37, AppliedLimitBytes: 70}
		if recreate {
			stub.user = nil
			part.Remote = nil
			part.Traffic.RemoteID = "41"
		} else {
			stub.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(5)
			stub.user["lastTrafficResetAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		remote, err = p.Ensure(context.Background(), "owner", part)
		if err != nil {
			t.Fatal(err)
		}
		if remote.LimitBytes != 33 || remote.Status != "ACTIVE" {
			t.Fatalf("remaining budget not enforced: %+v", remote)
		}
		part.Traffic = &bundle.TrafficState{UsedBytes: 70, ObservedTotalBytes: 70, RemoteID: remote.ID, RemoteUsedBytes: 33, AppliedLimitBytes: 70}
		part.Remote = &remote
		stub.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(0)
		stub.user["lastTrafficResetAt"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
		remote, err = p.Ensure(context.Background(), "owner", part)
		if err != nil {
			t.Fatal(err)
		}
		if remote.LimitBytes != 1 || remote.Status != "DISABLED" {
			t.Fatalf("exhausted counter reset unlocked traffic: %+v", remote)
		}
	}
}
