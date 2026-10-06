package smartrouter

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTextRecoveryDemandThenSyntheticAndSuccessReset(t *testing.T) {
	cfg := DefaultQualityConfig()
	cfg.TextRecoveryProbeAfterMS = 300_000
	state := InitialQuality(false)
	started := int64(1_000_000)
	now := started
	for i, seconds := range []int64{2, 4, 8, 16, 32, 64, 128, 46} {
		state = ReduceQuality(state, QualityEvent{AtMS: now, StartedAtMS: now - 1, ObservationID: fmt.Sprint(i), Outcome: OutcomeInfrastructure, DemandRecovery: true, Probe: true}, cfg)
		require.Equal(t, seconds*1000, state.NextProbeAtMS-now)
		require.Equal(t, started, state.RecoveryStartedMS)
		require.Equal(t, uint64(i+1), state.RealHardFailures)
		require.Zero(t, state.TotalProbeFailures)
		now = state.NextProbeAtMS
	}
	require.Equal(t, started+300_000, now)
	for _, delay := range []int64{300_000, 600_000, 600_000} {
		state = ReduceQuality(state, QualityEvent{AtMS: now, Outcome: OutcomeInfrastructure, DemandRecovery: true, Probe: true, SyntheticProbe: true}, cfg)
		require.Equal(t, delay, state.NextProbeAtMS-now)
		normalized, _ := NormalizeAutomaticRecoveryState(state, now+1)
		require.Equal(t, state.NextProbeAtMS, normalized.NextProbeAtMS)
		now = state.NextProbeAtMS
	}
	require.Equal(t, uint64(8), state.RealHardFailures, "synthetic failures never become customer failures")
	for _, synthetic := range []bool{false, true} {
		recovered := ReduceQuality(state, QualityEvent{AtMS: now, ObservationID: "real-fixed-group-or-probe", Outcome: OutcomeSuccess, Probe: synthetic, SyntheticProbe: synthetic, DemandRecovery: synthetic}, cfg)
		require.Equal(t, QualityHealthy, recovered.Phase)
		require.Zero(t, recovered.RecoveryStartedMS)
		require.Zero(t, recovered.DemandRecoveryFailures)
		require.Zero(t, recovered.NextProbeAtMS)
		recovered = ReduceQuality(recovered, QualityEvent{AtMS: now + 2, StartedAtMS: now + 1, ObservationID: "new", Outcome: OutcomeInfrastructure, DemandRecovery: true}, cfg)
		require.Equal(t, int64(2000), recovered.NextProbeAtMS-(now+2))
	}
}

func TestTextRecoveryPlannerHonorsCoolingAndFiveMinuteHandoff(t *testing.T) {
	input := basePlanInput()
	input.TextRecoveryProbeAfterMS = 300_000
	entry := input.Catalog.Contracts[testContractID]
	pool := entry.Pools[testPoolID]
	pool.Candidates = pool.Candidates[:1]
	entry.Pools[testPoolID] = pool
	input.Catalog.Contracts[testContractID] = entry
	started := input.NowMS
	q := QualityState{Phase: QualityOpen, DemandRecovery: true, RecoveryStartedMS: started, LastFailureMS: started, RealHardFailures: 1, NextProbeAtMS: started + 2000, Epoch: 1}
	input.Quality.Routes["route-cheap"] = q
	input.Policy.HealthGuard = false
	_, err := Plan(input)
	require.Error(t, err)
	input.NowMS = started + 2000
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, AdmissionProbe, plan.Admission)
	input.NowMS = started + 300_000
	_, err = Plan(input)
	require.Error(t, err, "after five minutes only background probes or fixed-group success recover the route")
}
