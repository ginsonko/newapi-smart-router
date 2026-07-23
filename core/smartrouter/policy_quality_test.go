package smartrouter

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicyJSONNormalizeAndValidate(t *testing.T) {
	raw := `{
		"version":3,
		"policy_id":"balanced-v1",
		"enabled":true,
		"pool_policy":"balanced-v1",
		"order_mode":"auto_price",
		"max_effective_ratio_ppm":50000,
		"excluded_groups":["z","a","z"],
		"excluded_routes":[],
		"contract_overrides":{},
		"health_guard":true,
		"recovery_profile":"balanced",
		"ttft_policy":{"enabled":false,"metric":"p95","target_ms":2500,"hard_max_ms":6000,"min_samples":8},
		"queue_policy":{"mode":"no_queue","max_wait_ms":0,"min_saving_percent":0},
		"allow_future_certified_routes":true
	}`
	var policy Policy
	require.NoError(t, json.Unmarshal([]byte(raw), &policy))
	policy = NormalizePolicy(policy)
	require.NoError(t, policy.Validate())
	assert.Equal(t, []string{"a", "z"}, policy.ExcludedGroups)
}

func TestNormalizePolicyDoesNotInventPriceOrPoolAuthority(t *testing.T) {
	policy := NormalizePolicy(Policy{})
	assert.Zero(t, policy.MaxEffectiveRatioPPM)
	assert.Empty(t, policy.PoolPolicy)
	assert.Equal(t, QueueModeNoQueue, policy.QueuePolicy.Mode)
	require.Error(t, policy.Validate())
}

func TestV4FixedGroupScopeRequiresAnExplicitSelection(t *testing.T) {
	policy := NormalizePolicy(Policy{
		Version: PolicyVersion, Enabled: true, Strategy: StrategyPrice,
		MaxEffectiveRatioPPM: 1, HealthGuard: true,
	})
	assert.ErrorContains(t, policy.Validate(), "fixed v4 group scope")

	policy.ManualGroupOrder = []string{"cheap"}
	assert.NoError(t, policy.Validate())
}

func TestQualityColdStartAndRecoveryTransitions(t *testing.T) {
	config := DefaultQualityConfig()
	require.NoError(t, config.Validate())

	bootstrap := InitialQuality(true)
	assert.Equal(t, QualityBootstrap, bootstrap.Phase)
	bootstrap = ReduceQuality(bootstrap, QualityEvent{AtMS: 1_000, Outcome: OutcomeSuccess}, config)
	assert.Equal(t, QualityHealthy, bootstrap.Phase)
	assert.Equal(t, int64(1_000_000), bootstrap.WarmingTrafficPPM)
	assert.Zero(t, bootstrap.WarmingEpoch)

	opened := ReduceQuality(InitialQuality(true), QualityEvent{AtMS: 2_000, Outcome: OutcomeInfrastructure}, config)
	assert.Equal(t, QualityOpen, opened.Phase)
	halfOpen, acquired := BeginProbe(opened, opened.NextProbeAtMS)
	assert.True(t, acquired)
	assert.Equal(t, QualityHalfOpen, halfOpen.Phase)
	_, acquired = BeginProbe(halfOpen, opened.NextProbeAtMS+1)
	assert.False(t, acquired)

	recovering := ReduceQuality(halfOpen, QualityEvent{
		AtMS: opened.NextProbeAtMS + 2, Outcome: OutcomeSuccess, Probe: true, SyntheticProbe: true,
	}, config)
	assert.Equal(t, QualityWarming, recovering.Phase)
	assert.Equal(t, int64(100_000), recovering.WarmingTrafficPPM)
	assert.Equal(t, uint64(1), recovering.WarmingEpoch)
	assert.Zero(t, recovering.SuccessfulWarmSamples, "a probe success opens canary traffic but is not a real sample")
	for sample := 1; sample <= config.HealthyWarmSamples; sample++ {
		recovering = ReduceQuality(recovering, QualityEvent{
			AtMS: opened.NextProbeAtMS + int64(sample)*1_000, Outcome: OutcomeSuccess,
		}, config)
	}
	assert.Equal(t, QualityHealthy, recovering.Phase)
	assert.Equal(t, int64(1_000_000), recovering.WarmingTrafficPPM)
}

func TestProbeEvidenceIsSeparateFromTrafficEvidence(t *testing.T) {
	config := DefaultQualityConfig()
	state := ReduceQuality(InitialQuality(false), QualityEvent{
		AtMS: 1000, Outcome: OutcomeSuccess, Probe: true, HTTPStatus: 200,
	}, config)
	assert.Equal(t, int64(1000), state.LastProbeAtMS)
	assert.Equal(t, OutcomeSuccess, state.LastProbeOutcome)
	assert.Equal(t, 200, state.LastProbeHTTPStatus)
	assert.Equal(t, uint64(1), state.ProbeSuccesses)

	state = ReduceQuality(state, QualityEvent{
		AtMS: 2000, Outcome: OutcomeInfrastructure, Probe: true,
		HTTPStatus: 503, ErrorSummary: "upstream unavailable",
	}, config)
	assert.Equal(t, int64(2000), state.LastProbeAtMS)
	assert.Equal(t, OutcomeInfrastructure, state.LastProbeOutcome)
	assert.Equal(t, 503, state.LastProbeHTTPStatus)
	assert.Equal(t, "upstream unavailable", state.LastProbeErrorSummary)
	assert.Equal(t, uint64(1), state.ProbeSuccesses)
}

func TestSuccessfulRecoveryStartsFreshBackoffEpisode(t *testing.T) {
	config := DefaultQualityConfig()
	state := QualityState{
		Phase: QualityHalfOpen, Epoch: 1, OpenCount: 9,
		NextProbeAtMS: 99_000, LastHTTPStatus: 503,
		LastErrorSummary: "old outage",
	}

	recovered := ReduceQuality(state, QualityEvent{
		AtMS: 100_000, Outcome: OutcomeSuccess, Probe: true,
	}, config)
	assert.Equal(t, QualityWarming, recovered.Phase)
	assert.Zero(t, recovered.OpenCount)
	assert.Zero(t, recovered.NextProbeAtMS)
	assert.Zero(t, recovered.LastHTTPStatus)
	assert.Empty(t, recovered.LastErrorSummary)

	failedAgain := ReduceQuality(recovered, QualityEvent{
		AtMS: 101_000, Outcome: OutcomeInfrastructure,
	}, config)
	assert.Equal(t, QualityOpen, failedAgain.Phase)
	assert.Equal(t, 1, failedAgain.OpenCount)
	assert.Equal(t, int64(161_000), failedAgain.NextProbeAtMS)
}

func TestSyntheticProbeMovesDegradedRouteIntoRealTrafficWarming(t *testing.T) {
	config := DefaultQualityConfig()
	state := QualityState{Phase: QualityHealthy, StableSinceMS: 1}
	state = ReduceQuality(state, QualityEvent{AtMS: 1_000, Outcome: OutcomeInfrastructure}, config)
	require.Equal(t, QualityDegraded, state.Phase)

	state = ReduceQuality(state, QualityEvent{
		AtMS: 2_000, Outcome: OutcomeSuccess, Probe: true, SyntheticProbe: true,
	}, config)
	assert.Equal(t, QualityWarming, state.Phase)
	assert.Zero(t, state.SuccessfulWarmSamples)
	assert.Equal(t, int64(100_000), state.WarmingTrafficPPM)
	assert.Equal(t, uint64(1), state.WarmingEpoch)
}

func TestVirginBootstrapRealSuccessBecomesImmediatelyHealthy(t *testing.T) {
	config := DefaultQualityConfig()
	state, acquired := BeginProbe(InitialQuality(false), 1_000)
	require.True(t, acquired)
	require.Equal(t, QualityHalfOpen, state.Phase)

	state = ReduceQuality(state, QualityEvent{
		AtMS: 2_000, Outcome: OutcomeSuccess, VirginBootstrap: true,
	}, config)
	assert.Equal(t, QualityHealthy, state.Phase)
	assert.Equal(t, uint64(1), state.RealSuccesses)
	assert.Zero(t, state.ProbeSuccesses)
	assert.Equal(t, int64(1_000_000), state.WarmingTrafficPPM)
	assert.Zero(t, state.SampleDueAtMS)
}

func TestQualityFailuresAndNeutralOutcomes(t *testing.T) {
	config := DefaultQualityConfig()
	healthy := QualityState{Phase: QualityHealthy, StableSinceMS: 1}

	unchanged := ReduceQuality(healthy, QualityEvent{AtMS: 10, Outcome: OutcomeCapacityLimited}, config)
	assert.Equal(t, healthy.Phase, unchanged.Phase)
	assert.Equal(t, healthy.ConsecutiveHardFailures, unchanged.ConsecutiveHardFailures)
	assert.Equal(t, OutcomeCapacityLimited, unchanged.LastOutcome)
	unchanged = ReduceQuality(healthy, QualityEvent{AtMS: 10, Outcome: OutcomeClientCancelled}, config)
	assert.Equal(t, healthy, unchanged)

	degraded := ReduceQuality(healthy, QualityEvent{AtMS: 20, Outcome: OutcomeInfrastructure}, config)
	assert.Equal(t, QualityDegraded, degraded.Phase)
	opened := ReduceQuality(degraded, QualityEvent{AtMS: 30, Outcome: OutcomeInfrastructure}, config)
	assert.Equal(t, QualityOpen, opened.Phase)
	assert.Equal(t, int64(60_030), opened.NextProbeAtMS)

	_, acquired := BeginProbe(opened, opened.NextProbeAtMS-1)
	assert.False(t, acquired)
	halfOpen, acquired := BeginProbe(opened, opened.NextProbeAtMS)
	assert.True(t, acquired)
	assert.Equal(t, QualityHalfOpen, halfOpen.Phase)
}

func TestBootstrapHardFailureImmediatelyOpens(t *testing.T) {
	state := ReduceQuality(InitialQuality(true), QualityEvent{AtMS: 50, Outcome: OutcomeInfrastructure}, DefaultQualityConfig())
	assert.Equal(t, QualityOpen, state.Phase)
	assert.Equal(t, int64(60_050), state.NextProbeAtMS)
}

func TestDeterministicAdmissionHasNoProcessRandomness(t *testing.T) {
	first := DeterministicAdmission("session", "route", 7, 500_000)
	for iteration := 0; iteration < 10; iteration++ {
		assert.Equal(t, first, DeterministicAdmission("session", "route", 7, 500_000))
	}
	assert.False(t, DeterministicAdmission("", "route", 7, 1_000_000))
}

func TestQualitySeparatesRealTrafficAndProbeEvidenceAndReliability(t *testing.T) {
	config := DefaultQualityConfig()
	state := InitialQuality(false)
	state = ReduceQuality(state, QualityEvent{AtMS: 1_000, Outcome: OutcomeSuccess}, config)
	state = ReduceQuality(state, QualityEvent{AtMS: 2_000, Outcome: OutcomeInfrastructure}, config)
	state = ReduceQuality(state, QualityEvent{AtMS: 3_000, Outcome: OutcomeSuccess, Probe: true}, config)
	state = ReduceQuality(state, QualityEvent{AtMS: 4_000, Outcome: OutcomeInfrastructure, Probe: true}, config)

	assert.Equal(t, uint64(1), state.TotalSuccesses)
	assert.Equal(t, uint64(1), state.TotalHardFailures)
	assert.Equal(t, uint64(1), state.RealSuccesses)
	assert.Equal(t, uint64(1), state.RealHardFailures)
	assert.Equal(t, uint64(1), state.ProbeSuccesses)
	assert.Equal(t, uint64(1), state.TotalProbeFailures)
	assert.Equal(t, int64(1_000), state.LastRealSuccessMS)
	assert.Equal(t, int64(2_000), state.LastRealFailureMS)
	assert.Equal(t, OutcomeInfrastructure, state.LastRealOutcome)
	assert.Equal(t, int64(4_000), state.LastProbeAtMS)
	assert.Equal(t, uint64(2), state.ReliabilitySamples)
}

func TestLegacyAutomaticQuarantineBecomesImmediatelyRecoverable(t *testing.T) {
	state, changed := NormalizeAutomaticRecoveryState(QualityState{
		Phase: QualityQuarantined, Epoch: 7, Revision: 11, NextProbeAtMS: math.MaxInt64,
	}, 42_000)
	require.True(t, changed)
	assert.Equal(t, QualityOpen, state.Phase)
	assert.Equal(t, int64(42_000), state.NextProbeAtMS)
	assert.Equal(t, uint64(7), state.Epoch)
	assert.Equal(t, uint64(12), state.Revision)
}

func TestAutomaticRecoveryNeverEndsAndUsesFiniteFastThenSlowDeadlines(t *testing.T) {
	config := DefaultQualityConfig()
	state := InitialQuality(false)
	state = ReduceQuality(state, QualityEvent{AtMS: 1_000, Outcome: OutcomeInfrastructure, Probe: true}, config)
	assert.Equal(t, int64(61_000), state.NextProbeAtMS)
	assert.NotEqual(t, int64(math.MaxInt64), state.NextProbeAtMS)

	state = ReduceQuality(state, QualityEvent{
		AtMS:    1_000 + config.FastRecoveryWindowMS + 1,
		Outcome: OutcomeInfrastructure, Probe: true,
	}, config)
	assert.Equal(t, int64(1_000+config.FastRecoveryWindowMS+1+config.SlowRecoveryBackoffMS[0]), state.NextProbeAtMS)
	for attempt := 0; attempt < 20; attempt++ {
		state = ReduceQuality(state, QualityEvent{
			AtMS: state.NextProbeAtMS, Outcome: OutcomeInfrastructure, Probe: true,
		}, config)
		assert.Equal(t, QualityOpen, state.Phase)
		assert.Greater(t, state.NextProbeAtMS, state.LastFailureMS)
		assert.NotEqual(t, int64(math.MaxInt64), state.NextProbeAtMS)
	}
}

func TestRecoveredRouteWithGoodHistoryReturnsAfterOneRealCanary(t *testing.T) {
	config := DefaultQualityConfig()
	config.HealthyWarmSamples = 3
	config.FastCanaryMinSamples = 5
	config.FastCanaryMinReliabilityPPM = 800_000
	state := QualityState{
		Phase: QualityWarming, Epoch: 2, WarmingEpoch: 1,
		WarmingTrafficPPM: 250_000, ReliabilitySamples: 10,
		ReliabilityPPM: 850_000, RealSuccesses: 8, LastProbeAtMS: 1,
	}

	next := ReduceQuality(state, QualityEvent{AtMS: 2, Outcome: OutcomeSuccess}, config)
	assert.Equal(t, QualityHealthy, next.Phase)
	assert.Equal(t, int64(1_000_000), next.WarmingTrafficPPM)
}

func TestRecoveredRouteWithoutHistoryKeepsBoundedCanary(t *testing.T) {
	config := DefaultQualityConfig()
	config.HealthyWarmSamples = 3
	config.FastCanaryMinSamples = 5
	config.FastCanaryMinReliabilityPPM = 800_000
	state := QualityState{
		Phase: QualityWarming, Epoch: 2, WarmingEpoch: 1,
		WarmingTrafficPPM: 250_000, ReliabilitySamples: 1,
		ReliabilityPPM: 600_000, LastProbeAtMS: 1,
	}

	next := ReduceQuality(state, QualityEvent{AtMS: 2, Outcome: OutcomeSuccess}, config)
	assert.Equal(t, QualityWarming, next.Phase)
	assert.Equal(t, 1, next.SuccessfulWarmSamples)
}
