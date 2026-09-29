package periods

import (
	"errors"
	"fmt"
	"time"
)

type Period struct {
	CycleIndex int64
	Start      time.Time
	End        time.Time
}

func Current(subscriptionStart, subscriptionEnd, at time.Time, cycleDays int) (Period, bool, error) {
	if cycleDays <= 0 {
		return Period{}, false, errors.New("quota cycle days must be positive")
	}
	start := subscriptionStart.UTC()
	end := subscriptionEnd.UTC()
	now := at.UTC()
	if !end.After(start) {
		return Period{}, false, errors.New("subscription end must be after start")
	}
	if now.Before(start) || !now.Before(end) {
		return Period{}, false, nil
	}
	cycleDuration := time.Duration(cycleDays) * 24 * time.Hour
	if cycleDuration/time.Hour/24 != time.Duration(cycleDays) {
		return Period{}, false, fmt.Errorf("quota cycle duration overflows for %d days", cycleDays)
	}
	index := int64(now.Sub(start) / cycleDuration)
	periodStart := start.Add(time.Duration(index) * cycleDuration)
	periodEnd := periodStart.Add(cycleDuration)
	if periodEnd.After(end) {
		periodEnd = end
	}
	return Period{CycleIndex: index, Start: periodStart, End: periodEnd}, true, nil
}
