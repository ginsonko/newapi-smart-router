package smartrouter

import "errors"

type CapacitySnapshot struct {
	Version string                   `json:"version"`
	Domains map[string]CapacityState `json:"domains"`
}

type CapacityState struct {
	Known           bool  `json:"known"`
	MaxInflight     int   `json:"max_inflight"`
	Inflight        int   `json:"inflight"`
	BlockedUntilMS  int64 `json:"blocked_until_ms"`
	EstimatedWaitMS int64 `json:"estimated_wait_ms"`
}

type CapacityDecisionKind string

const (
	CapacityTryNow CapacityDecisionKind = "try_now"
	CapacitySpill  CapacityDecisionKind = "spill"
	CapacityQueue  CapacityDecisionKind = "queue"
)

type CapacityDecision struct {
	Kind            CapacityDecisionKind
	MaxQueueWaitMS  int64
	MaxInflightHint int
}

type capacityChoice struct {
	Kind           PlanKind
	Candidate      Candidate
	MaxQueueWaitMS int64
}

func DecideCapacity(state CapacityState, policy QueuePolicy, nowMS, deadlineMS int64, admission AdmissionKind) CapacityDecision {
	limitHint := admissionCapacityLimit(admission)
	if capacityAvailable(state, nowMS) {
		return CapacityDecision{Kind: CapacityTryNow, MaxInflightHint: limitHint}
	}
	if policy.Mode != QueueModeShortWait || !queueableAdmission(admission) || state.EstimatedWaitMS <= 0 || state.EstimatedWaitMS > policy.MaxWaitMS {
		return CapacityDecision{Kind: CapacitySpill, MaxInflightHint: limitHint}
	}
	if deadlineMS <= nowMS || state.EstimatedWaitMS >= deadlineMS-nowMS {
		return CapacityDecision{Kind: CapacitySpill, MaxInflightHint: limitHint}
	}
	return CapacityDecision{Kind: CapacityQueue, MaxQueueWaitMS: state.EstimatedWaitMS, MaxInflightHint: limitHint}
}

func capacityAvailable(state CapacityState, nowMS int64) bool {
	if !state.Known {
		return true
	}
	if state.MaxInflight < 0 || state.Inflight < 0 {
		return false
	}
	if state.BlockedUntilMS > nowMS {
		return false
	}
	return state.MaxInflight <= 0 || state.Inflight < state.MaxInflight
}

func queueableAdmission(admission AdmissionKind) bool {
	return admission == AdmissionNormal || admission == AdmissionDegraded
}

func admissionCapacityLimit(admission AdmissionKind) int {
	switch admission {
	case AdmissionBootstrap, AdmissionVirginBootstrap, AdmissionProbe, AdmissionWarmSample:
		return 1
	default:
		return 0
	}
}

func chooseCapacity(candidates []Candidate, snapshot CapacitySnapshot, policy QueuePolicy, nowMS, deadlineMS int64, requeued bool) (capacityChoice, error) {
	if len(candidates) == 0 {
		return capacityChoice{}, ErrNoCandidate
	}
	var queued *capacityChoice
	for _, candidate := range candidates {
		state := snapshot.Domains[candidate.Route.CapacityDomain]
		decision := DecideCapacity(state, policy, nowMS, deadlineMS, candidate.Admission)
		switch decision.Kind {
		case CapacityTryNow:
			if queued != nil && shouldPreferQueue(queued.Candidate, candidate, policy.MinSavingPercent) {
				return *queued, nil
			}
			return capacityChoice{Kind: PlanAttempt, Candidate: candidate}, nil
		case CapacityQueue:
			if !requeued && queued == nil {
				queued = &capacityChoice{Kind: PlanQueue, Candidate: candidate, MaxQueueWaitMS: decision.MaxQueueWaitMS}
			}
		}
	}
	if queued != nil && !requeued {
		return *queued, nil
	}
	return capacityChoice{}, errors.New("smartrouter: all eligible routes are capacity constrained")
}

func shouldPreferQueue(queued, immediate Candidate, minSavingPercent int) bool {
	if immediate.RatioPPM <= 0 || queued.RatioPPM >= immediate.RatioPPM {
		return false
	}
	difference := immediate.RatioPPM - queued.RatioPPM
	// ceil(total * percent / 100), split before multiplying to avoid overflow.
	percent := int64(minSavingPercent)
	minimumDifference := (immediate.RatioPPM/100)*percent + ((immediate.RatioPPM%100)*percent+99)/100
	return difference >= minimumDifference
}
