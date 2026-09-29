package bundle

import "math"

func addBytes(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// ProjectTraffic uses the provider's lifetime counter, not packet metering.
// Legacy rows acquire a lifetime anchor without rewriting historical charges.
func ProjectTraffic(part Part, remote Remote) (TrafficState, error) {
	if remote.UsedBytes < 0 || remote.LimitBytes < 0 || (remote.LifetimeUsedBytes != nil && (*remote.LifetimeUsedBytes < 0 || *remote.LifetimeUsedBytes < remote.UsedBytes)) {
		return TrafficState{}, ErrAccounting
	}
	var previous *TrafficState
	if part.Traffic != nil {
		copy := *part.Traffic
		previous = &copy
	} else if part.Remote != nil {
		r := part.Remote
		previous = &TrafficState{UsedBytes: r.UsedBytes, ObservedTotalBytes: r.UsedBytes, RemoteID: r.ID, RemoteUsedBytes: r.UsedBytes, LastResetAt: r.LastTrafficResetAt, AppliedLimitBytes: r.LimitBytes, LifetimeUsedBytes: r.LifetimeUsedBytes}
	}
	if previous == nil {
		used, total := remote.UsedBytes, remote.UsedBytes
		if remote.LifetimeUsedBytes != nil {
			total = *remote.LifetimeUsedBytes
			if part.ResetStrategy == "NO_RESET" || part.ResetStrategy == "" {
				used = total
			}
		}
		// A committed create may have timed out before the first observation.
		// A subsequent native reset must not hide usage from that owned user.
		return TrafficState{UsedBytes: used, ObservedTotalBytes: total, RemoteID: remote.ID, RemoteUsedBytes: remote.UsedBytes, LastResetAt: remote.LastTrafficResetAt, AppliedLimitBytes: remote.LimitBytes, LifetimeUsedBytes: remote.LifetimeUsedBytes}, nil
	}
	newUser := previous.RemoteID != remote.ID
	reset := newUser || (remote.LastTrafficResetAt != nil && (previous.LastResetAt == nil || remote.LastTrafficResetAt.After(*previous.LastResetAt)))
	if !newUser && previous.LastResetAt != nil && (remote.LastTrafficResetAt == nil || remote.LastTrafficResetAt.Before(*previous.LastResetAt)) {
		return *previous, ErrAccounting
	}
	// A counter regression without a new reset epoch must never be counted twice.
	if !reset && remote.UsedBytes < previous.RemoteUsedBytes {
		return *previous, ErrAccounting
	}
	delta := remote.UsedBytes - previous.RemoteUsedBytes
	if reset {
		delta = remote.UsedBytes
	}
	if !newUser && previous.LifetimeUsedBytes != nil {
		if remote.LifetimeUsedBytes == nil || *remote.LifetimeUsedBytes < *previous.LifetimeUsedBytes {
			return *previous, ErrAccounting
		}
		delta = *remote.LifetimeUsedBytes - *previous.LifetimeUsedBytes
		if (!reset && delta != remote.UsedBytes-previous.RemoteUsedBytes) || (reset && delta < remote.UsedBytes) {
			return *previous, ErrAccounting
		}
	} else if newUser && remote.LifetimeUsedBytes != nil {
		delta = *remote.LifetimeUsedBytes
	}
	previous.ObservedTotalBytes = addBytes(previous.ObservedTotalBytes, delta)
	if part.ResetStrategy == "NO_RESET" || part.ResetStrategy == "" {
		previous.UsedBytes = addBytes(previous.UsedBytes, delta)
	} else {
		previous.UsedBytes = remote.UsedBytes
	}
	if reset {
		previous.CounterResets++
	}
	previous.RemoteID, previous.RemoteUsedBytes, previous.LastResetAt, previous.LifetimeUsedBytes = remote.ID, remote.UsedBytes, remote.LastTrafficResetAt, remote.LifetimeUsedBytes
	return *previous, nil
}

// EnforcementPart translates a logical remaining package into a native cap.
// Zero is unlimited upstream, so an exhausted user is disabled with a nonzero cap.
func EnforcementPart(part Part, remote Remote) (Part, error) {
	traffic, err := ProjectTraffic(part, remote)
	if err != nil {
		return part, err
	}
	if part.LimitBytes == 0 || (part.ResetStrategy != "NO_RESET" && part.ResetStrategy != "") {
		return part, nil
	}
	remaining := max(int64(0), part.LimitBytes-traffic.UsedBytes)
	if remaining == 0 {
		part.Enabled = false
	}
	// Only bytes charged in previous counter epochs reduce the native cap.
	// Clamping the remaining balance before adding the current counter would
	// raise the cap to the observed overrun after exhaustion (50 MB -> 77 MB).
	// Keep the original threshold even when the panel reports more than it.
	previouslyCharged := max(int64(0), traffic.UsedBytes-remote.UsedBytes)
	part.LimitBytes = max(int64(1), part.LimitBytes-previouslyCharged)
	return part, nil
}

func observe(part *Part, remote Remote, applied bool) error {
	traffic, err := ProjectTraffic(*part, remote)
	if err != nil {
		part.AccountingStatus = "anomaly"
		return err
	}
	if applied {
		traffic.AppliedLimitBytes = part.LimitBytes
	}
	part.Traffic, part.Remote = &traffic, &remote
	part.AccountingStatus = "ok"
	return nil
}

func UsedBytes(part Part) int64 {
	if part.Traffic != nil {
		return part.Traffic.UsedBytes
	}
	if part.Remote != nil {
		return part.Remote.UsedBytes
	}
	return 0
}
