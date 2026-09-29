package usage

import (
	"context"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/remnawave"
)

type fakeReader struct{}

func (fakeReader) SquadUserDailyUsage(context.Context, string, int64, time.Time, time.Time) (remnawave.DailyUsage, error) {
	return remnawave.DailyUsage{Days: []remnawave.UsageDay{{Date: "2026-09-05", Nodes: []remnawave.NodeUsage{{UUID: "node", TotalBytes: 10}}}}}, nil
}
func TestDailyProviderFlagsInexactRange(t *testing.T) {
	p := NewRemnawaveDailyProvider(fakeReader{})
	start := time.Date(2026, 9, 5, 12, 30, 0, 0, time.UTC)
	got, err := p.Usage(context.Background(), Request{UserID: 42, SquadUUID: "squad", Start: start, End: start.Add(time.Hour)})
	if err != nil || got.TotalBytes != 10 || got.ExactRange {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
