package smartrouter

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThreeDistinctConfirmedFailuresOpenAndOneProbeSuccessRecovers(t *testing.T) {
	config := DefaultQualityConfig()
	state := QualityState{Phase: QualityHealthy, Epoch: 1, LastSuccessMS: 1, LastOutcome: OutcomeSuccess}

	failedAt := int64(1_000)
	state = ReduceQuality(state, QualityEvent{
		AtMS: failedAt, StartedAtMS: failedAt, ObservationID: "request-a", Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	require.Equal(t, QualityDegraded, state.Phase, "one fluctuation remains globally usable")
	assert.Zero(t, state.NextProbeAtMS)

	duplicate := ReduceQuality(state, QualityEvent{
		AtMS: failedAt + 1, StartedAtMS: failedAt, ObservationID: "request-a", Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	assert.Equal(t, state, duplicate, "one request replay cannot increase the shared failure counter")

	state = ReduceQuality(state, QualityEvent{
		AtMS: failedAt + 2, StartedAtMS: failedAt + 2, ObservationID: "request-b", Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	require.Equal(t, QualityDegraded, state.Phase)
	state = ReduceQuality(state, QualityEvent{
		AtMS: failedAt + 3, StartedAtMS: failedAt + 3, ObservationID: "request-c", Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	require.Equal(t, QualityOpen, state.Phase, "three distinct confirmed failures open shared health")
	assert.Equal(t, failedAt+3, state.NextProbeAtMS, "the first recovery opportunity is immediate")

	probeAt := state.NextProbeAtMS
	state, acquired := BeginProbe(state, probeAt)
	require.True(t, acquired)
	require.Equal(t, QualityHalfOpen, state.Phase)

	state = ReduceQuality(state, QualityEvent{
		AtMS: probeAt + 1, Outcome: OutcomeSuccess, Probe: true, SyntheticProbe: true,
	}, config)
	assert.Equal(t, QualityHealthy, state.Phase, "one successful probe must restore the route immediately")
	assert.Zero(t, state.NextProbeAtMS, "a healthy route must not retain another scheduled probe")
	assert.Zero(t, state.SampleDueAtMS, "recovery must not create a warming-sample gate")
}

func TestFailedProbeCadenceNeverExceedsFiveMinutes(t *testing.T) {
	config := DefaultQualityConfig()
	state := QualityState{
		Phase: QualityOpen, Epoch: 1, RecoveryStartedMS: 1,
		LastFailureMS: 1, LastOutcome: OutcomeInfrastructure,
	}
	probeAt := int64(2 * 60 * 60_000)

	state = ReduceQuality(state, QualityEvent{
		AtMS: probeAt, Outcome: OutcomeInfrastructure, Probe: true,
		RetryAfterMS: 2 * 60 * 60_000, HTTPStatus: 503,
	}, config)
	require.Equal(t, QualityOpen, state.Phase)
	assert.Greater(t, state.NextProbeAtMS, probeAt)
	assert.LessOrEqual(t, state.NextProbeAtMS-probeAt, int64(5*60_000),
		"provider retry-after and slow recovery tiers must not postpone the next probe beyond five minutes")
}

func TestNewerRealSuccessSuppressesAnOlderLateFailure(t *testing.T) {
	config := DefaultQualityConfig()
	state := QualityState{Phase: QualityHealthy, Epoch: 7, LastSuccessMS: 100, LastOutcome: OutcomeSuccess}

	state = ReduceQuality(state, QualityEvent{
		AtMS: 300, StartedAtMS: 200, ObservationID: "request-first-failure",
		Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	require.Equal(t, QualityDegraded, state.Phase)

	state = ReduceQuality(state, QualityEvent{
		AtMS: 400, StartedAtMS: 350, ObservationID: "request-newer-success",
		Outcome: OutcomeSuccess,
	}, config)
	require.Equal(t, QualityHealthy, state.Phase)
	require.Equal(t, int64(400), state.LastSuccessMS)
	require.Equal(t, 0, state.ConsecutiveHardFailures)

	late := ReduceQuality(state, QualityEvent{
		AtMS: 500, StartedAtMS: 300, ObservationID: "request-stale-late-failure",
		Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	assert.Equal(t, state, late,
		"a request which started before the newer proven success cannot reopen shared health when it finishes late")
}

func TestConfiguredSharedOpenThresholdIsHonored(t *testing.T) {
	config := DefaultQualityConfig()
	config.SharedOpenFailureThreshold = 4
	state := QualityState{Phase: QualityHealthy, Epoch: 1, LastSuccessMS: 1, LastOutcome: OutcomeSuccess}

	for index := 0; index < 3; index++ {
		atMS := int64(1_000 + index)
		state = ReduceQuality(state, QualityEvent{
			AtMS: atMS, StartedAtMS: atMS,
			ObservationID: fmt.Sprintf("request-%d", index),
			Outcome:       OutcomeInfrastructure, HTTPStatus: 503,
		}, config)
		assert.Equal(t, QualityDegraded, state.Phase)
		assert.Zero(t, state.NextProbeAtMS)
	}

	state = ReduceQuality(state, QualityEvent{
		AtMS: 1_003, StartedAtMS: 1_003, ObservationID: "request-3",
		Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	assert.Equal(t, QualityOpen, state.Phase)
	assert.Equal(t, 4, state.ConsecutiveHardFailures)
	assert.Equal(t, int64(1_003), state.NextProbeAtMS)
}

func TestLegacyRecoveryPhasesNormalizeWithoutARecoveryGate(t *testing.T) {
	nowMS := int64(1_000_000)
	tests := []struct {
		name  string
		state QualityState
		phase QualityPhase
	}{
		{
			name: "warming with newer success is healthy",
			state: QualityState{Phase: QualityWarming, Epoch: 1, LastFailureMS: 10, LastSuccessMS: 20,
				LastOutcome: OutcomeSuccess, NextProbeAtMS: nowMS + 3_600_000},
			phase: QualityHealthy,
		},
		{
			name: "degraded with fewer than three failures remains degraded",
			state: QualityState{Phase: QualityDegraded, Epoch: 1, LastSuccessMS: 10, LastFailureMS: 20,
				LastOutcome: OutcomeInfrastructure, ConsecutiveHardFailures: 2, NextProbeAtMS: nowMS + 3_600_000},
			phase: QualityDegraded,
		},
		{
			name:  "bootstrap without evidence is unknown",
			state: QualityState{Phase: QualityBootstrap, Epoch: 1},
			phase: QualityUnknown,
		},
		{
			name:  "degraded without evidence is unknown",
			state: QualityState{Phase: QualityDegraded, Epoch: 9, NextProbeAtMS: nowMS + 3_600_000},
			phase: QualityUnknown,
		},
		{
			name:  "open without evidence is unknown",
			state: QualityState{Phase: QualityOpen, Epoch: 9},
			phase: QualityUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, changed := NormalizeAutomaticRecoveryState(test.state, nowMS)
			require.True(t, changed)
			assert.Equal(t, test.phase, normalized.Phase)
			if normalized.Phase == QualityOpen {
				assert.GreaterOrEqual(t, normalized.NextProbeAtMS, nowMS)
				assert.LessOrEqual(t, normalized.NextProbeAtMS-nowMS, int64(5*60_000))
			}
			if normalized.Phase == QualityHealthy || normalized.Phase == QualityUnknown {
				assert.Zero(t, normalized.NextProbeAtMS)
				assert.Zero(t, normalized.SampleDueAtMS)
			}
		})
	}
}
