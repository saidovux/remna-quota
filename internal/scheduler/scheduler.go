package scheduler

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Intervals struct {
	Normal, Fast, Critical, Exhausted time.Duration
	FastPercent, CriticalPercent      float64
}

func NextInterval(ratio float64, exhausted bool, cfg Intervals) time.Duration {
	if exhausted {
		return cfg.Exhausted
	}
	percent := ratio * 100
	if percent >= cfg.CriticalPercent {
		return cfg.Critical
	}
	if percent >= cfg.FastPercent {
		return cfg.Fast
	}
	return cfg.Normal
}

type Job struct{ Run func(context.Context) error }

type BatchResult struct{ Attempted, Failed int }

func RunBatch(ctx context.Context, jobs []Job, maxConcurrent int) BatchResult {
	if maxConcurrent <= 0 {
		return BatchResult{Failed: len(jobs)}
	}
	workerCount := min(maxConcurrent, len(jobs))
	queue := make(chan Job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	result := BatchResult{}
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				if job.Run == nil || job.Run(ctx) != nil {
					mu.Lock()
					result.Failed++
					mu.Unlock()
				}
			}
		}()
	}
	for _, job := range jobs {
		select {
		case queue <- job:
			result.Attempted++
		case <-ctx.Done():
			skipped := len(jobs) - result.Attempted
			close(queue)
			wg.Wait()
			result.Failed += skipped
			return result
		}
	}
	close(queue)
	wg.Wait()
	return result
}

type Leader interface{ Release(context.Context) error }
type Locker interface {
	TryLeaderLock(context.Context) (Leader, bool, error)
}

var ErrNotLeader = errors.New("scheduler instance is not leader")
