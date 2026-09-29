package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestNextInterval(t *testing.T) {
	cfg := Intervals{Normal: 5 * time.Minute, Fast: time.Minute, Critical: 25 * time.Second, Exhausted: time.Minute, FastPercent: 80, CriticalPercent: 95}
	if NextInterval(.79, false, cfg) != cfg.Normal || NextInterval(.80, false, cfg) != cfg.Fast || NextInterval(.95, false, cfg) != cfg.Critical || NextInterval(1, true, cfg) != cfg.Exhausted {
		t.Fatal("unexpected interval")
	}
}
func TestRunBatchBounded(t *testing.T) {
	var current, maxSeen atomic.Int32
	jobs := make([]Job, 20)
	for i := range jobs {
		jobs[i] = Job{Run: func(context.Context) error {
			now := current.Add(1)
			for {
				old := maxSeen.Load()
				if now <= old || maxSeen.CompareAndSwap(old, now) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			current.Add(-1)
			return nil
		}}
	}
	result := RunBatch(context.Background(), jobs, 3)
	if result.Failed != 0 || maxSeen.Load() > 3 {
		t.Fatalf("result=%+v max=%d", result, maxSeen.Load())
	}
}
