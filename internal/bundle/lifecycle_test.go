package bundle

import (
	"testing"
	"time"
)

func futurePart(key string, limit, used int64, expiresInDays int, now time.Time) Part {
	return Part{
		PartRequest: PartRequest{
			Key:           key,
			Label:         key,
			Provider:      "remnawave",
			Profile:       key,
			LimitBytes:    limit,
			ExpiresAt:     now.AddDate(0, 0, expiresInDays),
			Enabled:       true,
			ResetStrategy: "NO_RESET",
		},
		Remote: &Remote{ID: "1", Username: key, Status: "ACTIVE", UsedBytes: used, LimitBytes: limit},
	}
}

func TestEvaluate_ActiveAccountStaysActive(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := Bundle{Enabled: true, Parts: []Part{futurePart("main", 1000, 100, 10, now)}}
	if d := Evaluate(&b, now); d != DecisionNone {
		t.Fatalf("decision=%v want none", d)
	}
	if b.Lifecycle != LifecycleActive || b.GraceUntil != nil {
		t.Fatalf("lifecycle=%q grace=%v", b.Lifecycle, b.GraceUntil)
	}
}

func TestEvaluate_ExhaustedUsesPendingPeriod(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := Bundle{Enabled: true,
		Parts:             []Part{futurePart("main", 1000, 1000, 10, now)},
		PendingPeriods:    2,
		RenewalPeriodDays: 30,
		RenewalParts:      []PeriodPart{{Key: "main", LimitBytes: 1000, Enabled: true, ResetStrategy: "NO_RESET"}},
	}
	rev := b.Revision
	if d := Evaluate(&b, now); d != DecisionActivated {
		t.Fatalf("decision=%v want activated", d)
	}
	if b.PendingPeriods != 1 {
		t.Fatalf("pending=%d want 1", b.PendingPeriods)
	}
	if b.Parts[0].LimitBytes != 2000 {
		t.Fatalf("limit=%d want 2000", b.Parts[0].LimitBytes)
	}
	if b.Revision != rev+1 {
		t.Fatalf("revision=%d want %d", b.Revision, rev+1)
	}
	if b.GraceUntil != nil || b.Lifecycle != LifecycleActive {
		t.Fatalf("lifecycle=%q grace=%v", b.Lifecycle, b.GraceUntil)
	}
}

func TestEvaluate_AutorenewChargesBalance(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := Bundle{Enabled: true,
		Parts:             []Part{futurePart("main", 1000, 1000, 10, now)},
		Autorenew:         true,
		BalanceMinor:      6000,
		RenewalPriceMinor: 15000,
		RenewalPeriodDays: 30,
		RenewalParts:      []PeriodPart{{Key: "main", LimitBytes: 1000, Enabled: true}},
	}
	if d := Evaluate(&b, now); d != DecisionGrace {
		t.Fatalf("insufficient balance decision=%v want grace", d)
	}
	if b.BalanceMinor != 6000 {
		t.Fatalf("balance=%d want untouched 6000", b.BalanceMinor)
	}
	if b.GraceUntil == nil {
		t.Fatal("grace not set")
	}

	b.BalanceMinor = 20000
	b.GraceUntil = nil
	if d := Evaluate(&b, now); d != DecisionActivated {
		t.Fatalf("decision=%v want activated", d)
	}
	if b.BalanceMinor != 5000 {
		t.Fatalf("balance=%d want 5000", b.BalanceMinor)
	}
}

func TestEvaluate_GraceThenDelete(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := Bundle{Enabled: true, Parts: []Part{futurePart("main", 1000, 1000, -1, now)}}
	if d := Evaluate(&b, now); d != DecisionGrace {
		t.Fatalf("decision=%v want grace", d)
	}
	grace := *b.GraceUntil
	if d := Evaluate(&b, grace.Add(-time.Hour)); d != DecisionNone {
		t.Fatalf("decision=%v want none within grace", d)
	}
	if d := Evaluate(&b, grace.Add(time.Hour)); d != DecisionDelete {
		t.Fatalf("decision=%v want delete", d)
	}
}

func TestActivatePeriod_CreatesMissingPart(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := Bundle{Enabled: true,
		RenewalPeriodDays: 30,
		RenewalParts: []PeriodPart{
			{Key: "main", Provider: "remnawave", Profile: "main", LimitBytes: 1000, Enabled: true},
			{Key: "cdn", Provider: "remnawave", Profile: "cdn", LimitBytes: 500, Enabled: true},
		},
	}
	ActivatePeriod(&b, now)
	if len(b.Parts) != 2 {
		t.Fatalf("parts=%d want 2", len(b.Parts))
	}
	if b.Parts[0].ExpiresAt.UTC() != now.AddDate(0, 0, 30).UTC() {
		t.Fatalf("expiry=%v", b.Parts[0].ExpiresAt)
	}
	if b.Parts[1].LimitBytes != 500 {
		t.Fatalf("cdn limit=%d want 500", b.Parts[1].LimitBytes)
	}
}
