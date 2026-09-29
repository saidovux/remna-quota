package identity

import (
	"context"
	"testing"

	"github.com/saidovux/remna-quota/internal/remnawave"
)

type fakeUsers struct{ byID, byShort int }

func (f *fakeUsers) User(_ context.Context, id int64) (remnawave.User, error) {
	f.byID++
	return remnawave.User{ID: id}, nil
}
func (f *fakeUsers) UserByShortUUID(_ context.Context, s string) (remnawave.User, error) {
	f.byShort++
	return remnawave.User{ID: 42, ShortUUID: s}, nil
}

func TestResolvePrefersNumericID(t *testing.T) {
	f := &fakeUsers{}
	r := New(f)
	id := int64(42)
	user, err := r.Resolve(context.Background(), Source{NumericUserID: &id, ShortUUID: "short"})
	if err != nil || user.ID != 42 || f.byID != 1 || f.byShort != 0 {
		t.Fatalf("user=%+v calls=%d/%d err=%v", user, f.byID, f.byShort, err)
	}
}
func TestResolveShortUUID(t *testing.T) {
	f := &fakeUsers{}
	r := New(f)
	user, err := r.Resolve(context.Background(), Source{ShortUUID: "short"})
	if err != nil || user.ID != 42 || f.byShort != 1 {
		t.Fatalf("user=%+v err=%v", user, err)
	}
}
func TestResolveFailsClosedWithoutStableIdentity(t *testing.T) {
	r := New(&fakeUsers{})
	if _, err := r.Resolve(context.Background(), Source{}); err == nil {
		t.Fatal("expected identity error")
	}
}
