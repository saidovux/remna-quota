package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/config"
	"github.com/saidovux/remna-quota/internal/quota"
	"github.com/saidovux/remna-quota/internal/remnawave"
)

func TestMainExhaustedDoesNotTouchCDN(t *testing.T) {
	user := testUser(mainSquad, cdnSquad)
	desired := quota.DesiredMembership(quota.DesiredInput{SubscriptionActive: true, UserActive: true, TariffContainsPool: true, State: quota.Exhausted})
	actions := PlanPool(user, pools()["main"], desired)
	if len(actions) != 1 || actions[0].SquadUUID != mainSquad || actions[0].Desired != quota.Absent {
		t.Fatalf("actions=%+v", actions)
	}
}

func TestCDNExhaustedDoesNotTouchMain(t *testing.T) {
	user := testUser(mainSquad, cdnSquad)
	desired := quota.DesiredMembership(quota.DesiredInput{SubscriptionActive: true, UserActive: true, TariffContainsPool: true, State: quota.Exhausted})
	actions := PlanPool(user, pools()["cdn"], desired)
	if len(actions) != 1 || actions[0].SquadUUID != cdnSquad {
		t.Fatalf("actions=%+v", actions)
	}
}

func TestDryRunPlansWithoutMutation(t *testing.T) {
	fake := &fakeClient{user: testUser(mainSquad)}
	r, err := New(fake, "dry-run", pools())
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Apply(context.Background(), Action{PoolKey: "main", SquadUUID: mainSquad, UserID: 42, Desired: quota.Absent})
	if err != nil || !result.Planned || fake.removes != 0 {
		t.Fatalf("result=%+v removes=%d err=%v", result, fake.removes, err)
	}
}

func TestAcceptedMutationIsPolledUntilObserved(t *testing.T) {
	fake := &fakeClient{user: testUser(mainSquad), applyMutation: true}
	r, _ := New(fake, "live", pools())
	r.backoff = []time.Duration{time.Millisecond}
	result, err := r.Apply(context.Background(), Action{PoolKey: "main", SquadUUID: mainSquad, UserID: 42, Desired: quota.Absent})
	if err != nil || !result.Changed || !result.Verified || fake.removes != 1 {
		t.Fatalf("result=%+v removes=%d err=%v", result, fake.removes, err)
	}
}

func TestAmbiguousMutationRereadsWithoutDuplicate(t *testing.T) {
	fake := &fakeClient{user: testUser(mainSquad), applyMutation: true, mutationErr: errors.New("timeout")}
	r, _ := New(fake, "live", pools())
	result, err := r.Apply(context.Background(), Action{PoolKey: "main", SquadUUID: mainSquad, UserID: 42, Desired: quota.Absent})
	if err != nil || !result.RecoveredAmbiguity || fake.removes != 1 {
		t.Fatalf("result=%+v removes=%d err=%v", result, fake.removes, err)
	}
}

func TestExternalReaddIsRemovedAgain(t *testing.T) {
	fake := &fakeClient{user: testUser(mainSquad), applyMutation: true}
	r, _ := New(fake, "live", pools())
	r.backoff = []time.Duration{time.Millisecond}
	action := Action{PoolKey: "main", SquadUUID: mainSquad, UserID: 42, Desired: quota.Absent}
	if _, err := r.Apply(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	fake.user = testUser(mainSquad)
	if _, err := r.Apply(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	if fake.removes != 2 {
		t.Fatalf("removes=%d", fake.removes)
	}
}

func TestUnmanagedSquadRejected(t *testing.T) {
	fake := &fakeClient{user: testUser("admin-squad")}
	r, _ := New(fake, "live", pools())
	_, err := r.Apply(context.Background(), Action{PoolKey: "main", SquadUUID: "admin-squad", UserID: 42, Desired: quota.Absent})
	if err == nil {
		t.Fatal("expected unmanaged squad error")
	}
}

func TestRemnawaveUnavailableCausesNoMutation(t *testing.T) {
	fake := &fakeClient{userErr: errors.New("unavailable")}
	r, _ := New(fake, "live", pools())
	_, err := r.Apply(context.Background(), Action{PoolKey: "main", SquadUUID: mainSquad, UserID: 42, Desired: quota.Absent})
	if err == nil || fake.removes != 0 || fake.adds != 0 {
		t.Fatalf("adds=%d removes=%d err=%v", fake.adds, fake.removes, err)
	}
}

const (
	mainSquad = "11111111-1111-4111-8111-111111111111"
	cdnSquad  = "22222222-2222-4222-8222-222222222222"
)

func pools() map[string]config.TrafficPool {
	return map[string]config.TrafficPool{"main": {Key: "main", ManagedSquadUUIDs: []string{mainSquad}}, "cdn": {Key: "cdn", ManagedSquadUUIDs: []string{cdnSquad}}}
}
func testUser(squads ...string) remnawave.User {
	u := remnawave.User{ID: 42, ShortUUID: "short", Username: "synthetic", Status: "ACTIVE"}
	for _, id := range squads {
		u.ActiveInternalSquads = append(u.ActiveInternalSquads, remnawave.InternalSquad{UUID: id, Name: "synthetic"})
	}
	return u
}

type fakeClient struct {
	user          remnawave.User
	userErr       error
	applyMutation bool
	mutationErr   error
	adds, removes int
}

func (f *fakeClient) User(context.Context, int64) (remnawave.User, error) {
	return f.user, f.userErr
}
func (f *fakeClient) AddUsersToSquad(_ context.Context, squad string, _ []int64) error {
	f.adds++
	if f.applyMutation && !f.user.InSquad(squad) {
		f.user.ActiveInternalSquads = append(f.user.ActiveInternalSquads, remnawave.InternalSquad{UUID: squad, Name: "synthetic"})
	}
	return f.mutationErr
}
func (f *fakeClient) RemoveUsersFromSquad(_ context.Context, squad string, _ []int64) error {
	f.removes++
	if f.applyMutation {
		out := f.user.ActiveInternalSquads[:0]
		for _, s := range f.user.ActiveInternalSquads {
			if s.UUID != squad {
				out = append(out, s)
			}
		}
		f.user.ActiveInternalSquads = out
	}
	return f.mutationErr
}
