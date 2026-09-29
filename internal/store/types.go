package store

import (
	"time"

	"github.com/saidovux/remna-quota/internal/quota"
)

type Subscription struct {
	ID                     int64
	BedolagaSubscriptionID string
	BedolagaUserID         *string
	RemnawaveUserID        *int64
	RemnawaveShortUUID     *string
	TariffKey              string
	StartAt                time.Time
	EndAt                  time.Time
	Status                 string
	LastSyncedAt           time.Time
}

type PoolSeed struct {
	Key                 string
	LimitBytes          int64
	AccountingSquadUUID string
	InitialDecision     quota.AccessDecision
}

type PoolState struct {
	ID                  int64
	QuotaPeriodID       int64
	PoolKey             string
	LimitBytes          int64
	UsedBytesHighWater  int64
	State               quota.State
	LastAccessDecision  quota.AccessDecision
	AccountingSquadUUID string
	ExhaustedAt         *time.Time
	LastUsageCheckAt    *time.Time
	LastGoodUsageAt     *time.Time
	UpdatedAt           time.Time
}

type ActionLog struct {
	SubscriptionID       *int64
	RemnawaveUserID      *int64
	PoolKey              *string
	Action               string
	PreviousState        *string
	DesiredState         *string
	RequestCorrelationID *string
	Success              bool
	ErrorCode            *string
	ErrorMessage         *string
}
