package smartrouter

import (
	"fmt"
	"testing"

	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func imageQualityEvent(observationID string, outcome QualityOutcome, atMS int64) QualityEvent {
	return QualityEvent{ImageAttempt: true, ObservationID: observationID, Outcome: outcome, AtMS: atMS, StartedAtMS: atMS}
}

func imageQualityFailures(count int, atMS int64) QualityState {
	state := InitialQuality(false)
	for index := 0; index < count; index++ {
		state = ReduceQuality(state, imageQualityEvent(fmt.Sprint(index), OutcomeInfrastructure, atMS), DefaultQualityConfig())
	}
	return state
}

func TestImageQualityThresholdAndWindow(t *testing.T) {
	const nowMS int64 = 10_000_000
	config := DefaultQualityConfig()
	state := imageQualityFailures(10, nowMS)
	assert.False(t, state.ImageUnavailable)
	assert.Equal(t, QualityDegraded, state.Phase)
	state = ReduceQuality(state, imageQualityEvent("eleventh", OutcomeInfrastructure, nowMS), config)
	assert.True(t, state.ImageUnavailable)
	assert.Equal(t, QualityOpen, state.Phase)
	assert.Equal(t, uint64(11), state.RealHardFailures)
	encoded, err := json.Marshal(state)
	require.NoError(t, err)
	var restored QualityState
	require.NoError(t, json.Unmarshal(encoded, &restored))
	assert.True(t, state == restored)
	state, _ = NormalizeAutomaticRecoveryStateWithConfig(state, nowMS+(DefaultImageFailureWindowMS-1), config)
	assert.True(t, state.ImageUnavailable)
	state, _ = NormalizeAutomaticRecoveryStateWithConfig(state, nowMS+DefaultImageFailureWindowMS, config)
	assert.True(t, state.ImageUnavailable)
	state = ReduceQuality(state, imageQualityEvent("outside-window", OutcomeInfrastructure, nowMS+(DefaultImageFailureWindowMS+1)), config)
	assert.True(t, state.ImageUnavailable)
	assert.Equal(t, 1, state.ConsecutiveHardFailures)
	state = ReduceQuality(state, imageQualityEvent("late-pending", OutcomeImagePending, nowMS+(DefaultImageFailureWindowMS+2)), config)
	assert.True(t, state.ImageUnavailable)
	state = ReduceQuality(state, imageQualityEvent("recovered", OutcomeSuccess, nowMS+(DefaultImageFailureWindowMS+3)), config)
	assert.False(t, state.ImageUnavailable)
	state = imageQualityFailures(10, nowMS)
	state = ReduceQuality(state, imageQualityEvent("outside-window", OutcomeInfrastructure, nowMS+(DefaultImageFailureWindowMS+1)), config)
	assert.False(t, state.ImageUnavailable)
	assert.Equal(t, 1, state.ConsecutiveHardFailures)
}

func TestImageQualityPendingNeutralAndTerminalIdentity(t *testing.T) {
	const nowMS int64 = 10_000_000
	for _, neutral := range []QualityOutcome{OutcomeImagePending, OutcomeClientCancelled, "unknown"} {
		t.Run(string(neutral), func(test *testing.T) {
			config := DefaultQualityConfig()
			pending := imageQualityEvent("pending", neutral, nowMS+1)
			state := ReduceQuality(InitialQuality(false), pending, config)
			for index := 0; index < 11; index++ {
				state = ReduceQuality(state, imageQualityEvent(fmt.Sprint(index), OutcomeInfrastructure, nowMS+1), config)
			}
			assert.False(test, state.ImageUnavailable)
			assert.Equal(test, uint64(11), state.RealHardFailures)
			assert.Equal(test, state, ReduceQuality(state, pending, config))
			terminal := pending
			terminal.AtMS++
			terminal.Outcome = OutcomeInfrastructure
			state = ReduceQuality(state, terminal, config)
			assert.True(test, state.ImageUnavailable)
			assert.Equal(test, uint64(12), state.RealHardFailures)
			assert.Equal(test, state, ReduceQuality(state, terminal, config))
			assert.Equal(test, state, ReduceQuality(state, pending, config))
			terminal.Outcome = OutcomeSuccess
			state = ReduceQuality(state, terminal, config)
			assert.False(test, state.ImageUnavailable)
			assert.Equal(test, QualityHealthy, state.Phase)
			assert.Equal(test, uint64(1), state.RealSuccesses)
			assert.Equal(test, uint64(11), state.RealHardFailures)
			terminal.Outcome = OutcomeInfrastructure
			assert.Equal(test, state, ReduceQuality(state, terminal, config))
		})
	}
}

func TestImageQualityContentAndUserTerminalRemainNeutral(t *testing.T) {
	for _, outcome := range []QualityOutcome{OutcomeContentRejected, OutcomeUserRejected} {
		t.Run(string(outcome), func(test *testing.T) {
			config := DefaultQualityConfig()
			pending := imageQualityEvent("rejected", OutcomeImagePending, 1_000_000)
			state := ReduceQuality(InitialQuality(false), pending, config)
			terminal := pending
			terminal.Outcome = outcome
			state = ReduceQuality(state, terminal, config)
			assert.Contains(test, state.ImageAttemptEvidence, string(outcome))
			for index := 0; index < 11; index++ {
				state = ReduceQuality(state, imageQualityEvent(fmt.Sprint(index), OutcomeInfrastructure, pending.AtMS), config)
			}
			assert.False(test, state.ImageUnavailable)
			terminal.Outcome = OutcomeInfrastructure
			assert.Equal(test, state, ReduceQuality(state, terminal, config))
			assert.Equal(test, state, ReduceQuality(state, pending, config))
			terminal.Outcome = OutcomeSuccess
			state = ReduceQuality(state, terminal, config)
			assert.Equal(test, uint64(1), state.RealSuccesses)
		})
	}
}

func TestImageQualitySuccessSuppressesLaterFailuresUntilWindowExpires(t *testing.T) {
	const nowMS int64 = 10_000_000
	config := DefaultQualityConfig()
	state := imageQualityFailures(11, nowMS)
	state = ReduceQuality(state, imageQualityEvent("fixed-success", OutcomeSuccess, nowMS+1000), config)
	require.Equal(t, QualityHealthy, state.Phase)
	for index := 0; index < 11; index++ {
		state = ReduceQuality(state, imageQualityEvent(fmt.Sprintf("later-%d", index), OutcomeInfrastructure, nowMS+2000), config)
	}
	state, _ = NormalizeAutomaticRecoveryStateWithConfig(state, nowMS+(DefaultImageFailureWindowMS+999), config)
	assert.False(t, state.ImageUnavailable)
	state, _ = NormalizeAutomaticRecoveryStateWithConfig(state, nowMS+(DefaultImageFailureWindowMS+1000), config)
	assert.True(t, state.ImageUnavailable)
	assert.Equal(t, 11, state.ConsecutiveHardFailures)
	state = ReduceQuality(state, imageQualityEvent("smart-success", OutcomeSuccess, nowMS+(DefaultImageFailureWindowMS+1000)), config)
	assert.False(t, state.ImageUnavailable)
	assert.Equal(t, QualityHealthy, state.Phase)
}

func TestImageQualityUsesStartTimeAndIgnoresSyntheticEvidence(t *testing.T) {
	const nowMS int64 = 10_000_000
	config := DefaultQualityConfig()
	state := imageQualityFailures(10, nowMS)
	pending := imageQualityEvent("long-running", OutcomeImagePending, nowMS-DefaultImageFailureWindowMS)
	state = ReduceQuality(state, pending, config)
	terminal := pending
	terminal.AtMS = nowMS
	terminal.Outcome = OutcomeInfrastructure
	state = ReduceQuality(state, terminal, config)
	assert.False(t, state.ImageUnavailable)
	assert.Equal(t, 10, state.ConsecutiveHardFailures)
	terminal.ObservationID = "current"
	terminal.StartedAtMS = nowMS
	state = ReduceQuality(state, terminal, config)
	require.True(t, state.ImageUnavailable)
	synthetic := imageQualityEvent("probe", OutcomeSuccess, nowMS+1)
	synthetic.SyntheticProbe = true
	assert.Equal(t, state, ReduceQuality(state, synthetic, config))
	terminal.Outcome = OutcomeSuccess
	terminal.ObservationID = "long-running"
	terminal.StartedAtMS = nowMS - DefaultImageFailureWindowMS
	state = ReduceQuality(state, terminal, config)
	assert.False(t, state.ImageUnavailable)
}

func TestImageQualityPendingSurvivesLeaderboardSizedFailureBurst(t *testing.T) {
	const nowMS int64 = 10_000_000
	config := DefaultQualityConfig()
	state := ReduceQuality(InitialQuality(false), imageQualityEvent("pending", OutcomeImagePending, nowMS), config)
	for index := 0; index < 101; index++ {
		state = ReduceQuality(state, imageQualityEvent(fmt.Sprint(index), OutcomeInfrastructure, nowMS+1), config)
	}
	assert.False(t, state.ImageUnavailable)
	state = ReduceQuality(state, imageQualityEvent("pending", OutcomeInfrastructure, nowMS+2), config)
	assert.True(t, state.ImageUnavailable)
	assert.Equal(t, uint64(102), state.RealHardFailures)
	duplicate := imageQualityEvent("0", OutcomeInfrastructure, nowMS+3)
	assert.Equal(t, state, ReduceQuality(state, duplicate, config))
}

func TestImageQualityCustomConfigAndCorruptEvidence(t *testing.T) {
	config := DefaultQualityConfig()
	config.ImageFailureWindowMS = 1000
	config.ImageFailureThreshold = 2
	require.NoError(t, config.Validate())
	state := ReduceQuality(InitialQuality(false), imageQualityEvent("first", OutcomeInfrastructure, 10_000), config)
	assert.False(t, state.ImageUnavailable)
	state = ReduceQuality(state, imageQualityEvent("second", OutcomeInfrastructure, 10_001), config)
	assert.True(t, state.ImageUnavailable)
	state, _ = NormalizeImageQuality(state, 11_000, QualityConfig{})
	assert.True(t, state.ImageUnavailable)
	state.ImageAttemptEvidence = "broken-json"
	state.ImageUnavailable = true
	state, _ = NormalizeImageQuality(state, 11_001, config)
	assert.True(t, state.ImageUnavailable)
	recovered := ReduceQuality(state, imageQualityEvent("repair-success", OutcomeSuccess, 11_002), config)
	assert.False(t, recovered.ImageUnavailable)
	assert.Equal(t, QualityHealthy, recovered.Phase)
	state.ImageUnavailable = false
	state, _ = NormalizeImageQuality(state, 11_001, config)
	assert.False(t, state.ImageUnavailable)
	config.ImageFailureThreshold = -1
	require.Error(t, config.Validate())
}

func TestImageQualityOldSuccessCannotClearLaterUnavailable(t *testing.T) {
	const nowMS int64 = 10_000_000
	config := DefaultQualityConfig()
	oldSuccess := imageQualityEvent("original-success", OutcomeSuccess, nowMS)
	state := ReduceQuality(InitialQuality(false), oldSuccess, config)
	state, _ = NormalizeImageQuality(state, nowMS+(DefaultImageFailureWindowMS+1), config)
	assert.Equal(t, state, ReduceQuality(state, oldSuccess, config))
	for index := 0; index < 11; index++ {
		state = ReduceQuality(state, imageQualityEvent(fmt.Sprint(index), OutcomeInfrastructure, nowMS+(DefaultImageFailureWindowMS+2)), config)
	}
	require.True(t, state.ImageUnavailable)
	assert.Equal(t, nowMS+(DefaultImageFailureWindowMS+2), state.ImageUnavailableAtMS)
	assert.Equal(t, state, ReduceQuality(state, oldSuccess, config))
	oldSuccess.ObservationID = "late-first-delivery"
	oldSuccess.AtMS = nowMS + 200_000
	assert.Equal(t, state, ReduceQuality(state, oldSuccess, config))
	state = ReduceQuality(state, imageQualityEvent("new-success", OutcomeSuccess, nowMS+(DefaultImageFailureWindowMS+3)), config)
	assert.False(t, state.ImageUnavailable)
	assert.Zero(t, state.ImageUnavailableAtMS)
	assert.Equal(t, uint64(2), state.RealSuccesses)
}

func TestImageQualityPrunedEventsCannotBeCountedAgain(t *testing.T) {
	config := DefaultQualityConfig()
	const nowMS int64 = 10_000_000
	for _, outcome := range []QualityOutcome{OutcomeImagePending, OutcomeClientCancelled, OutcomeContentRejected, OutcomeInfrastructure, OutcomeSuccess} {
		t.Run(string(outcome), func(test *testing.T) {
			event := imageQualityEvent("original", outcome, nowMS)
			state := ReduceQuality(InitialQuality(false), event, config)
			state, _ = NormalizeImageQuality(state, nowMS+config.ImageFailureWindowMS+1, config)
			assert.Equal(test, state, ReduceQuality(state, event, config))
			state, _ = NormalizeImageQuality(state, nowMS+2*config.ImageFailureWindowMS, config)
			assert.Equal(test, state, ReduceQuality(state, event, config))
			if outcome == OutcomeImagePending {
				event.Outcome = OutcomeSuccess
				event.AtMS = nowMS + 2*config.ImageFailureWindowMS
				state = ReduceQuality(state, event, config)
				assert.Equal(test, uint64(1), state.RealSuccesses)
				assert.Equal(test, state, ReduceQuality(state, event, config))
			}
		})
	}
}
