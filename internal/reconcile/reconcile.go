package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/saidovux/remna-quota/internal/config"
	"github.com/saidovux/remna-quota/internal/quota"
	"github.com/saidovux/remna-quota/internal/remnawave"
)

type Client interface {
	User(context.Context, int64) (remnawave.User, error)
	AddUsersToSquad(context.Context, string, []int64) error
	RemoveUsersFromSquad(context.Context, string, []int64) error
}

type Action struct {
	PoolKey   string
	SquadUUID string
	UserID    int64
	Desired   quota.AccessDecision
}

type Result struct {
	Planned            bool
	Changed            bool
	Verified           bool
	RecoveredAmbiguity bool
}

type Reconciler struct {
	client  Client
	live    bool
	owned   map[string]string
	backoff []time.Duration
}

func New(client Client, writeMode string, pools map[string]config.TrafficPool) (*Reconciler, error) {
	if client == nil {
		return nil, errors.New("reconcile client is required")
	}
	owned := map[string]string{}
	for poolKey, pool := range pools {
		for _, squadUUID := range pool.ManagedSquadUUIDs {
			if prior, exists := owned[squadUUID]; exists && prior != poolKey {
				return nil, fmt.Errorf("managed squad is owned by both %q and %q", prior, poolKey)
			}
			owned[squadUUID] = poolKey
		}
	}
	return &Reconciler{client: client, live: writeMode == "live", owned: owned, backoff: []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}}, nil
}

func PlanPool(user remnawave.User, pool config.TrafficPool, desired quota.DesiredResult) []Action {
	if !desired.Enforce {
		return nil
	}
	actions := make([]Action, 0, len(pool.ManagedSquadUUIDs))
	for _, squadUUID := range pool.ManagedSquadUUIDs {
		present := user.InSquad(squadUUID)
		if (desired.Decision == quota.Present && !present) || (desired.Decision == quota.Absent && present) {
			actions = append(actions, Action{PoolKey: pool.Key, SquadUUID: squadUUID, UserID: user.ID, Desired: desired.Decision})
		}
	}
	return actions
}

func (r *Reconciler) Apply(ctx context.Context, action Action) (Result, error) {
	owner, ok := r.owned[action.SquadUUID]
	if !ok || owner != action.PoolKey {
		return Result{}, errors.New("refusing mutation for an unmanaged squad")
	}
	if action.UserID <= 0 {
		return Result{}, errors.New("user ID must be positive")
	}
	if action.Desired != quota.Present && action.Desired != quota.Absent {
		return Result{}, errors.New("invalid desired membership")
	}

	user, err := r.client.User(ctx, action.UserID)
	if err != nil {
		return Result{}, fmt.Errorf("read actual membership: %w", err)
	}
	if observed(user, action) {
		return Result{Verified: true}, nil
	}
	if !r.live {
		return Result{Planned: true}, nil
	}

	if action.Desired == quota.Present {
		err = r.client.AddUsersToSquad(ctx, action.SquadUUID, []int64{action.UserID})
	} else {
		err = r.client.RemoveUsersFromSquad(ctx, action.SquadUUID, []int64{action.UserID})
	}
	if err != nil {
		userAfterError, readErr := r.client.User(ctx, action.UserID)
		if readErr == nil && observed(userAfterError, action) {
			return Result{Changed: true, Verified: true, RecoveredAmbiguity: true}, nil
		}
		return Result{}, fmt.Errorf("submit membership mutation: %w", err)
	}

	for _, delay := range r.backoff {
		if err := wait(ctx, delay); err != nil {
			return Result{Changed: true}, err
		}
		current, readErr := r.client.User(ctx, action.UserID)
		if readErr != nil {
			continue
		}
		if observed(current, action) {
			return Result{Changed: true, Verified: true}, nil
		}
	}
	return Result{Changed: true}, errors.New("membership mutation accepted but desired state was not observed before timeout")
}

func observed(user remnawave.User, action Action) bool {
	present := user.InSquad(action.SquadUUID)
	return (action.Desired == quota.Present && present) || (action.Desired == quota.Absent && !present)
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
