package periods

import (
	"testing"
	"time"
)

func TestRollingThirtyDayCycles(t *testing.T) {
	start := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	end := start.Add(90 * 24 * time.Hour)
	tests := []struct {
		at                 time.Time
		index              int64
		wantStart, wantEnd time.Time
	}{
		{start, 0, start, start.Add(30 * 24 * time.Hour)},
		{start.Add(30 * 24 * time.Hour), 1, start.Add(30 * 24 * time.Hour), start.Add(60 * 24 * time.Hour)},
		{start.Add(89 * 24 * time.Hour), 2, start.Add(60 * 24 * time.Hour), end},
	}
	for _, tt := range tests {
		period, active, err := Current(start, end, tt.at, 30)
		if err != nil || !active {
			t.Fatalf("Current(%s): active=%v err=%v", tt.at, active, err)
		}
		if period.CycleIndex != tt.index || !period.Start.Equal(tt.wantStart) || !period.End.Equal(tt.wantEnd) {
			t.Fatalf("period=%+v", period)
		}
	}
}

func TestExactSubscriptionEndIsInactive(t *testing.T) {
	start := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	_, active, err := Current(start, end, end, 30)
	if err != nil || active {
		t.Fatalf("active=%v err=%v", active, err)
	}
}

func TestEarlyRenewalDoesNotChangeCurrentPeriod(t *testing.T) {
	start := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	at := start.Add(15 * 24 * time.Hour)
	before, _, _ := Current(start, start.Add(30*24*time.Hour), at, 30)
	after, _, _ := Current(start, start.Add(90*24*time.Hour), at, 30)
	if before.CycleIndex != after.CycleIndex || !before.Start.Equal(after.Start) || !before.End.Equal(after.End) {
		t.Fatalf("before=%+v after=%+v", before, after)
	}
}

func TestFinalPartialCycleIsClamped(t *testing.T) {
	start := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(35 * 24 * time.Hour)
	period, active, err := Current(start, end, start.Add(32*24*time.Hour), 30)
	if err != nil || !active || !period.End.Equal(end) {
		t.Fatalf("period=%+v active=%v err=%v", period, active, err)
	}
}
