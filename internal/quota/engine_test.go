package quota

import "testing"

func TestPoolThresholdsAndHighWater(t *testing.T) {
	tests := []struct {
		name               string
		previous, observed int64
		wantUsed           int64
		wantState          State
	}{
		{"active", 0, 79, 79, Active}, {"warning", 0, 80, 80, Warning}, {"exhausted", 0, 100, 100, Exhausted}, {"monotonic", 90, 20, 90, Warning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.previous, tt.observed, 100, 80)
			if err != nil || got.UsedBytesHighWater != tt.wantUsed || got.State != tt.wantState {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestNewCycleStartsFromZero(t *testing.T) {
	got, err := Evaluate(0, 2, 100, 80)
	if err != nil || got.UsedBytesHighWater != 2 || got.State != Active {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestUpgradeAndDowngrade(t *testing.T) {
	upgrade, _ := Evaluate(180, 180, 500, 80)
	downgrade, _ := Evaluate(180, 180, 100, 80)
	if upgrade.State != Active || downgrade.State != Exhausted {
		t.Fatalf("upgrade=%s downgrade=%s", upgrade.State, downgrade.State)
	}
}

func TestDesiredMembership(t *testing.T) {
	if got := DesiredMembership(DesiredInput{SubscriptionActive: true, UserActive: true, TariffContainsPool: true, State: Active}); got.Decision != Present || !got.Enforce {
		t.Fatalf("got=%+v", got)
	}
	if got := DesiredMembership(DesiredInput{SubscriptionActive: true, UserActive: true, TariffContainsPool: true, State: Exhausted}); got.Decision != Absent || !got.Enforce {
		t.Fatalf("got=%+v", got)
	}
	if got := DesiredMembership(DesiredInput{State: Degraded, PreviousDecision: Present}); got.Decision != Present || got.Enforce {
		t.Fatalf("got=%+v", got)
	}
	if got := DesiredMembership(DesiredInput{State: Misconfigured, PreviousDecision: Absent}); got.Decision != Absent || got.Enforce {
		t.Fatalf("got=%+v", got)
	}
}
