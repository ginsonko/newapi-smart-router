package smartrouter

import "encoding/json"

type imageAttemptEvidence struct {
	StartedAtMS int64          `json:"started_at_ms"`
	AtMS        int64          `json:"at_ms"`
	Outcome     QualityOutcome `json:"outcome"`
}

func imageFailureOutcome(outcome QualityOutcome) bool {
	return outcome == OutcomeInfrastructure || outcome == OutcomeCredential || outcome == OutcomeCapacityLimited
}

func imageQualityEvidence(state QualityState) (map[string]imageAttemptEvidence, bool) {
	evidence := make(map[string]imageAttemptEvidence)
	if state.ImageAttemptEvidence != "" {
		if err := json.Unmarshal([]byte(state.ImageAttemptEvidence), &evidence); err != nil || evidence == nil {
			return nil, false
		}
	}
	return evidence, true
}

func ImageQualityEventAccepted(state QualityState, event QualityEvent) bool {
	if event.SyntheticProbe || event.ObservationID == "" {
		return false
	}
	if event.AtMS <= state.ImageEvidencePrunedBeforeMS {
		return false
	}
	if event.Outcome == OutcomeSuccess &&
		((state.ImageUnavailable && event.AtMS < state.ImageUnavailableAtMS) ||
			event.AtMS < state.ImageLastSuccessMS ||
			(event.AtMS == state.ImageLastSuccessMS && event.ObservationID == state.LastSuccessObservation)) {
		return false
	}
	evidence, valid := imageQualityEvidence(state)
	if !valid {
		return event.Outcome == OutcomeSuccess
	}
	previous, exists := evidence[event.ObservationID]
	if !exists {
		return true
	}
	if previous.Outcome == OutcomeSuccess || previous.Outcome == event.Outcome {
		return false
	}
	if previous.Outcome == OutcomeImagePending && event.Outcome == OutcomeClientCancelled {
		return false
	}
	if event.Outcome == OutcomeSuccess {
		return true
	}
	return previous.Outcome != OutcomeContentRejected && previous.Outcome != OutcomeUserRejected &&
		!imageFailureOutcome(previous.Outcome) && event.Outcome != OutcomeImagePending
}

func reduceImageQuality(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	if !ImageQualityEventAccepted(state, event) {
		return state
	}
	evidence, valid := imageQualityEvidence(state)
	if !valid {
		evidence = make(map[string]imageAttemptEvidence)
	}
	previous, exists := evidence[event.ObservationID]
	startedAtMS := event.StartedAtMS
	if startedAtMS <= 0 || startedAtMS > event.AtMS {
		startedAtMS = event.AtMS
	}
	if exists {
		startedAtMS = previous.StartedAtMS
	}
	if event.Outcome == OutcomeSuccess {
		state.ImageUnavailable = false
		state.ImageUnavailableAtMS = 0
		state.ImageLastSuccessMS = max64(state.ImageLastSuccessMS, event.AtMS)
		state.LastSuccessMS = max64(state.LastSuccessMS, event.AtMS)
		state.LastRealSuccessMS = max64(state.LastRealSuccessMS, event.AtMS)
		state.LastSuccessObservation = event.ObservationID
		state.TotalSuccesses++
		state.RealSuccesses++
		state.ConsecutiveSuccesses++
		if exists && imageFailureOutcome(previous.Outcome) {
			if state.TotalHardFailures > 0 {
				state.TotalHardFailures--
			}
			if state.RealHardFailures > 0 {
				state.RealHardFailures--
			}
		}
	} else if imageFailureOutcome(event.Outcome) {
		state.LastFailureMS = max64(state.LastFailureMS, event.AtMS)
		state.LastRealFailureMS = max64(state.LastRealFailureMS, event.AtMS)
		state.LastHardFailureObservation = event.ObservationID
		state.TotalHardFailures++
		state.RealHardFailures++
		state.ConsecutiveSuccesses = 0
	}
	evidence[event.ObservationID] = imageAttemptEvidence{StartedAtMS: startedAtMS, AtMS: event.AtMS, Outcome: event.Outcome}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return state
	}
	state.ImageHealthTracked = true
	state.ImageAttemptEvidence = string(encoded)
	if event.AtMS >= state.LastUpdatedMS {
		state.LastRealOutcome = event.Outcome
		state.LastOutcome = event.Outcome
		state.LastHTTPStatus = event.HTTPStatus
		state.LastErrorSummary = event.ErrorSummary
	}
	state.LastUpdatedMS = max64(state.LastUpdatedMS, event.AtMS)
	state.Revision++
	state, _ = NormalizeImageQuality(state, state.LastUpdatedMS, config)
	return state
}

func NormalizeImageQuality(state QualityState, nowMS int64, config QualityConfig) (QualityState, bool) {
	if !state.ImageHealthTracked {
		return state, false
	}
	original := state
	if config.ImageFailureWindowMS > 0 {
		state.ImageFailureWindowMS = config.ImageFailureWindowMS
	}
	if state.ImageFailureWindowMS <= 0 {
		state.ImageFailureWindowMS = DefaultImageFailureWindowMS
	}
	if config.ImageFailureThreshold > 0 {
		state.ImageFailureThreshold = config.ImageFailureThreshold
	}
	if state.ImageFailureThreshold <= 0 {
		state.ImageFailureThreshold = DefaultImageFailureThreshold
	}
	evidence, valid := imageQualityEvidence(state)
	cutoffMS := nowMS - state.ImageFailureWindowMS
	failures, total := 0, 0
	pruned := false
	for observationID, attempt := range evidence {
		if attempt.StartedAtMS <= cutoffMS && attempt.AtMS <= cutoffMS {
			delete(evidence, observationID)
			pruned = true
			continue
		}
		if attempt.StartedAtMS <= cutoffMS || attempt.StartedAtMS > nowMS {
			continue
		}
		total++
		if imageFailureOutcome(attempt.Outcome) {
			failures++
		}
	}
	if pruned {
		if encoded, err := json.Marshal(evidence); err == nil {
			state.ImageAttemptEvidence = string(encoded)
			state.ImageEvidencePrunedBeforeMS = max64(state.ImageEvidencePrunedBeforeMS, cutoffMS)
		}
	}
	state.ImageUnavailable = state.ImageUnavailable || (valid && total >= state.ImageFailureThreshold && failures == total &&
		(state.ImageLastSuccessMS == 0 || state.ImageLastSuccessMS <= cutoffMS))
	if state.ImageUnavailable && !original.ImageUnavailable {
		state.ImageUnavailableAtMS = nowMS
	}
	state.ConsecutiveHardFailures = failures
	state.NextProbeAtMS = 0
	state.SampleDueAtMS = 0
	state.DemandRecovery = false
	state.PassiveRecovery = false
	state.DemandRecoveryFailures = 0
	state.PassiveRecoveryFailures = 0
	state.SuccessfulWarmSamples = 0
	if state.Epoch == 0 {
		state.Epoch = 1
	}
	switch {
	case state.ImageUnavailable:
		state.Phase = QualityOpen
		state.WarmingTrafficPPM = 0
	case state.ImageLastSuccessMS > cutoffMS && state.ImageLastSuccessMS > 0:
		state.Phase = QualityHealthy
		state.WarmingTrafficPPM = 1_000_000
	case failures > 0:
		state.Phase = QualityDegraded
		state.WarmingTrafficPPM = 1_000_000
	default:
		state.Phase = QualityUnknown
		state.WarmingTrafficPPM = 0
	}
	if state == original {
		return state, false
	}
	state.Revision++
	state.LastUpdatedMS = max64(state.LastUpdatedMS, nowMS)
	return state, true
}
