package smartrouter

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMediaRecoveryExponentialCooldownAndRealEvidence(t *testing.T) {
	cfg := DefaultQualityConfig()
	state := InitialQuality(false)
	now := int64(1_000_000)
	for i, seconds := range []int64{60, 120, 240, 480, 960, 1920, 3600, 3600} {
		event := QualityEvent{AtMS: now, StartedAtMS: now - 1, ObservationID: fmt.Sprint(i), Outcome: OutcomeInfrastructure, PassiveRecovery: true, Probe: true}
		state = ReduceQuality(state, event, cfg)
		require.Equal(t, QualityOpen, state.Phase)
		require.Equal(t, seconds*1000, state.NextProbeAtMS-now)
		require.Equal(t, uint64(i+1), state.RealHardFailures)
		require.Zero(t, state.TotalProbeFailures)
		require.Zero(t, state.LastProbeAtMS)
		normalized, _ := NormalizeAutomaticRecoveryState(state, now+1)
		require.Equal(t, state.NextProbeAtMS, normalized.NextProbeAtMS, "snapshot must not truncate to text's five-minute ceiling")
		same := ReduceQuality(state, event, cfg)
		require.Equal(t, state, same, "same observation cannot double the cooldown")
		now = state.NextProbeAtMS
	}
	state = ReduceQuality(state, QualityEvent{AtMS: now, ObservationID: "success", Outcome: OutcomeSuccess, PassiveRecovery: true, Probe: true}, cfg)
	require.Equal(t, QualityHealthy, state.Phase)
	require.Equal(t, uint64(1), state.RealSuccesses)
	require.Zero(t, state.PassiveRecoveryFailures)
	require.Zero(t, state.NextProbeAtMS)
	state = ReduceQuality(state, QualityEvent{AtMS: now + 100, StartedAtMS: now + 1, ObservationID: "new-outage", Outcome: OutcomeInfrastructure, PassiveRecovery: true}, cfg)
	require.Equal(t, int64(60_000), state.NextProbeAtMS-(now+100))
	cfg.MediaRecoveryInitialBackoffMS = 2000
	cfg.MediaRecoveryMaxBackoffMS = 5000
	state = ReduceQuality(InitialQuality(false), QualityEvent{AtMS: now, Outcome: OutcomeInfrastructure, PassiveRecovery: true, RetryAfterMS: 99_000_000}, cfg)
	require.Equal(t, int64(5000), state.NextProbeAtMS-now)
}

func TestMediaRecoveryPlannerWaitsAndUsesRealHalfOpenLease(t *testing.T) {
	input := basePlanInput()
	entry := input.Catalog.Contracts[testContractID]
	pool := entry.Pools[testPoolID]
	pool.Candidates = pool.Candidates[:1]
	pool.Candidates[0].PassiveRecovery = true
	entry.Pools[testPoolID] = pool
	input.Catalog.Contracts[testContractID] = entry
	state := QualityState{Phase: QualityOpen, PassiveRecovery: true, NextProbeAtMS: input.NowMS + 1000, LastFailureMS: input.NowMS - 1, RealHardFailures: 1, Epoch: 1}
	input.Quality.Routes["route-cheap"] = state
	for _, guard := range []bool{true, false} {
		input.Policy.HealthGuard = guard
		_, err := Plan(input)
		require.Error(t, err, "text endpoint must not bypass media cooldown via last-resort or disabled health guard")
	}
	input.NowMS = state.NextProbeAtMS
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, AdmissionProbe, plan.Admission)
	state.Phase = QualityHalfOpen
	input.Quality.Routes["route-cheap"] = state
	_, err = Plan(input)
	require.Error(t, err)
	state.Phase = QualityOpen
	input.Quality.Routes["route-cheap"] = state
	input.Attempt.RecoveryProbeUsed = true
	_, err = Plan(input)
	require.Error(t, err, "one real media recovery per request")
}
