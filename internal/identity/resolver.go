package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/saidovux/remna-quota/internal/remnawave"
)

type UserReader interface {
	User(context.Context, int64) (remnawave.User, error)
	UserByShortUUID(context.Context, string) (remnawave.User, error)
}

type Source struct {
	NumericUserID *int64
	ShortUUID     string
}

type Resolver struct{ users UserReader }

func New(users UserReader) *Resolver { return &Resolver{users: users} }

func (r *Resolver) Resolve(ctx context.Context, source Source) (remnawave.User, error) {
	if r == nil || r.users == nil {
		return remnawave.User{}, errors.New("identity resolver has no Remnawave client")
	}
	if source.NumericUserID != nil {
		if *source.NumericUserID <= 0 {
			return remnawave.User{}, errors.New("Remnawave numeric user ID must be positive")
		}
		user, err := r.users.User(ctx, *source.NumericUserID)
		if err != nil {
			return remnawave.User{}, fmt.Errorf("resolve Remnawave numeric user ID: %w", err)
		}
		return user, nil
	}
	if source.ShortUUID != "" {
		user, err := r.users.UserByShortUUID(ctx, source.ShortUUID)
		if err != nil {
			return remnawave.User{}, fmt.Errorf("resolve Remnawave short UUID: %w", err)
		}
		return user, nil
	}
	return remnawave.User{}, errors.New("commercial subscription has no documented stable Remnawave identity")
}
