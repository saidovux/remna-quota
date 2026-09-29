package quota

import "fmt"

type State string

const (
	Active        State = "ACTIVE"
	Warning       State = "WARNING"
	Exhausted     State = "EXHAUSTED"
	Degraded      State = "DEGRADED"
	Misconfigured State = "MISCONFIGURED"
)

type AccessDecision string

const (
	Present AccessDecision = "PRESENT"
	Absent  AccessDecision = "ABSENT"
)

type Evaluation struct {
	UsedBytesHighWater int64
	State              State
}

func Evaluate(previousHighWater, observedBytes, limitBytes int64, warningPercent float64) (Evaluation, error) {
	if previousHighWater < 0 || observedBytes < 0 {
		return Evaluation{}, fmt.Errorf("usage cannot be negative")
	}
	if limitBytes <= 0 {
		return Evaluation{}, fmt.Errorf("limit must be positive")
	}
	if warningPercent <= 0 || warningPercent >= 100 {
		return Evaluation{}, fmt.Errorf("warning percent must be between 0 and 100")
	}
	effective := max(previousHighWater, observedBytes)
	state := Active
	if effective >= limitBytes {
		state = Exhausted
	} else if float64(effective) >= float64(limitBytes)*(warningPercent/100) {
		state = Warning
	}
	return Evaluation{UsedBytesHighWater: effective, State: state}, nil
}

func OnUsageFailure(previousHighWater int64) Evaluation {
	return Evaluation{UsedBytesHighWater: previousHighWater, State: Degraded}
}

type DesiredInput struct {
	SubscriptionActive bool
	UserActive         bool
	TariffContainsPool bool
	State              State
	PreviousDecision   AccessDecision
}

type DesiredResult struct {
	Decision AccessDecision
	Enforce  bool
}

func DesiredMembership(in DesiredInput) DesiredResult {
	if in.State == Degraded || in.State == Misconfigured {
		return DesiredResult{Decision: in.PreviousDecision, Enforce: false}
	}
	if in.SubscriptionActive && in.UserActive && in.TariffContainsPool && in.State != Exhausted {
		return DesiredResult{Decision: Present, Enforce: true}
	}
	return DesiredResult{Decision: Absent, Enforce: true}
}
