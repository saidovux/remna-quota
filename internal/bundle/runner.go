package bundle

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"
)

const workerCount = 4

type syncJob struct {
	done chan struct{}
	err  error
}
type runner struct {
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	jobs           map[string]*syncJob
	slots          chan struct{}
	interval       time.Duration
	wait           time.Duration
	wg             sync.WaitGroup
	closed         bool
	cursor         string
	runs, failures uint64
	seconds        float64
	operations     map[string]OperationMetrics
}

func newRunner() *runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &runner{ctx: ctx, cancel: cancel, jobs: map[string]*syncJob{}, slots: make(chan struct{}, workerCount), interval: 10 * time.Second, wait: 5 * time.Second, operations: map[string]OperationMetrics{}}
}

// Configure must be called before serving requests.
func (s *Service) Configure(interval time.Duration) {
	if interval > 0 {
		s.runner.interval = interval
	}
}
func (s *Service) Cancel() {
	r := s.runner
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()

}
func (s *Service) Close() { s.Cancel(); s.runner.wg.Wait() }

func (s *Service) startJob(id string, dueOnly bool) *syncJob {
	r := s.runner
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if job := r.jobs[id]; job != nil {
		return job
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return nil
	}
	job := &syncJob{done: make(chan struct{})}
	r.jobs[id] = job
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.ctx, 2*time.Minute)
		defer cancel()
		var err error
		// A newer revision is durable immediately. Re-read it after the old attempt,
		// without confirming old settings as applied to the new revision.
		for {
			var changed bool
			changed, err = s.syncOnce(ctx, id, dueOnly)
			if !changed || err != nil || ctx.Err() != nil {
				break
			}
			dueOnly = false
		}
		r.mu.Lock()
		job.err = err
		r.runs++
		r.seconds += time.Since(started).Seconds()
		if err != nil && !errors.Is(err, ErrBusy) {
			r.failures++
		}
		delete(r.jobs, id)
		<-r.slots
		close(job.done)
		r.mu.Unlock()
	}()
	return job
}

// Sync waits at most five seconds for work owned by the service, not the HTTP
// context. Durable pending intent remains discoverable after a crash or restart.
func (s *Service) Sync(ctx context.Context, id string) (Bundle, error) {
	if !accountIDPattern.MatchString(id) {
		return Bundle{}, ErrInvalid
	}
	if _, err := s.repo.Get(ctx, id); err != nil {
		return Bundle{}, err
	}
	job := s.startJob(id, false)
	if job == nil {
		return s.repo.Get(ctx, id)
	}
	timer := time.NewTimer(s.runner.wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return Bundle{}, ctx.Err()
	case <-timer.C:
		return s.repo.Get(ctx, id)
	case <-job.done:
		if job.err != nil && !errors.Is(job.err, ErrUnavailable) && !errors.Is(job.err, ErrBusy) {
			return Bundle{}, job.err
		}
		return s.repo.Get(ctx, id)
	}
}

// ScheduleDue dispatches available work without waiting for a provider. The
// cursor also prevents accounts locked by another replica from starving others.
func (s *Service) ScheduleDue(ctx context.Context) error {
	r := s.runner
	r.mu.Lock()
	after, closed := r.cursor, r.closed
	r.mu.Unlock()
	if closed {
		return context.Canceled
	}
	// Reap accounts whose grace period has expired before scheduling fresh work.
	_ = s.SweepDeleted(ctx)
	for {
		ids, err := s.repo.ListDue(ctx, s.now(), after, 100)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if s.startJob(id, true) == nil {
				return nil
			}
			after = id
			r.mu.Lock()
			r.cursor = after
			r.mu.Unlock()
		}
		if len(ids) < 100 {
			r.mu.Lock()
			r.cursor = ""
			r.mu.Unlock()
			return nil
		}
	}
}

// Reconcile explicitly retries all accounts; the runtime uses ScheduleDue.
func (s *Service) Reconcile(ctx context.Context) error    { return s.reconcile(ctx, false) }
func (s *Service) ReconcileDue(ctx context.Context) error { return s.reconcile(ctx, true) }
func (s *Service) reconcile(ctx context.Context, dueOnly bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tasks := make(chan string, workerCount)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range tasks {
				var job *syncJob
				for job == nil {
					if s.runner.ctx.Err() != nil {
						cancel()
						return
					}
					if ctx.Err() != nil {
						return
					}
					job = s.startJob(id, dueOnly)
					if job == nil {
						timer := time.NewTimer(25 * time.Millisecond)
						select {
						case <-ctx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-job.done:
					if job.err != nil && !errors.Is(job.err, ErrBusy) {
						mu.Lock()
						failures = append(failures, job.err)
						mu.Unlock()
					}
				}
			}
		}()
	}
	after := ""
	var listErr error
scan:
	for {
		var ids []string
		if dueOnly {
			ids, listErr = s.repo.ListDue(ctx, s.now(), after, 100)
		} else {
			var rows []Bundle
			rows, listErr = s.repo.List(ctx, ListOptions{Limit: 100, After: after})
			for _, row := range rows {
				ids = append(ids, row.ID)
			}
		}
		if listErr != nil {
			break
		}
		for _, id := range ids {
			select {
			case <-ctx.Done():
				listErr = ctx.Err()
				break scan
			case tasks <- id:
				after = id
			}
		}
		if len(ids) < 100 {
			break
		}
	}
	close(tasks)
	wg.Wait()
	if listErr != nil {
		failures = append(failures, listErr)
	}
	return errors.Join(failures...)
}

func retryDelay(failures int) time.Duration {
	shift := min(max(failures-1, 0), 9)
	base := min(time.Second*time.Duration(1<<shift), 5*time.Minute)
	// 80..100% jitter keeps the configured five-minute ceiling.
	return time.Duration(float64(base) * (0.8 + rand.Float64()*0.2))
}

func (s *Service) acquireRead(ctx context.Context, id string) (func(), error) {
	for {
		select {
		case s.runner.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ErrUnavailable
		case <-s.runner.ctx.Done():
			return nil, ErrUnavailable
		}
		release, err := s.repo.AcquireOperation(ctx, id)
		if err == nil {
			return func() { release(); <-s.runner.slots }, nil
		}
		<-s.runner.slots
		if !errors.Is(err, ErrBusy) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ErrUnavailable
		case <-timer.C:
		}
	}
}
