package usage

import (
	"context"
	"errors"
	"time"

	"github.com/saidovux/remna-quota/internal/remnawave"
)

type Request struct {
	UserID     int64
	SquadUUID  string
	Start, End time.Time
}
type Result struct {
	TotalBytes int64
	ExactRange bool
}

type Provider interface {
	Usage(context.Context, Request) (Result, error)
}

type DailyReader interface {
	SquadUserDailyUsage(context.Context, string, int64, time.Time, time.Time) (remnawave.DailyUsage, error)
}

type RemnawaveDailyProvider struct{ reader DailyReader }

func NewRemnawaveDailyProvider(reader DailyReader) *RemnawaveDailyProvider {
	return &RemnawaveDailyProvider{reader: reader}
}

func (p *RemnawaveDailyProvider) Usage(ctx context.Context, req Request) (Result, error) {
	if p == nil || p.reader == nil {
		return Result{}, errors.New("usage provider has no Remnawave reader")
	}
	if req.UserID <= 0 || req.SquadUUID == "" || !req.End.After(req.Start) {
		return Result{}, errors.New("invalid usage request")
	}
	daily, err := p.reader.SquadUserDailyUsage(ctx, req.SquadUUID, req.UserID, req.Start, req.End)
	if err != nil {
		return Result{}, err
	}
	total, err := daily.TotalBytes()
	if err != nil {
		return Result{}, err
	}
	// Remnawave 3.4.3 accepts only inclusive calendar dates. It is not exact
	// for arbitrary timestamp-aligned rolling periods.
	return Result{TotalBytes: total, ExactRange: false}, nil
}
