package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV4StrategiesArePerPolicyAndDeterministic(t *testing.T) {
	base := basePlanInput()
	base.Quality.Routes["route-cheap"] = QualityState{
		Phase: QualityHealthy, Epoch: 1, ReliabilityPPM: 300_000, ReliabilitySamples: 20,
	}
	base.Quality.Routes["route-plus"] = QualityState{
		Phase: QualityHealthy, Epoch: 1, ReliabilityPPM: 950_000, ReliabilitySamples: 20,
	}
	base.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
		"route-cheap": {Samples: 20, P95MS: 4_000, FreshUntilMS: base.NowMS + 1_000},
		"route-plus":  {Samples: 20, P95MS: 500, FreshUntilMS: base.NowMS + 1_000},
	}}

	tests := []struct {
		name     string
		strategy Strategy
		mutate   func(*PlanInput)
		want     string
	}{
		{name: "price", strategy: StrategyPrice, want: "route-cheap"},
		{name: "stability", strategy: StrategyStability, want: "route-plus"},
		{name: "latency", strategy: StrategyLatency, want: "route-plus"},
		{
			name: "balanced", strategy: StrategyBalanced, want: "route-plus",
			mutate: func(input *PlanInput) {
				input.Policy.BalancedWeights = BalancedWeights{Stability: 80, Price: 10, TTFT: 10}
			},
		},
		{
			name: "manual", strategy: StrategyManual, want: "route-plus",
			mutate: func(input *PlanInput) { input.Policy.ManualGroupOrder = []string{"plus", "cheap"} },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Policy = NormalizePolicy(base.Policy)
			input.Policy.Strategy = test.strategy
			input.Policy.OrderMode = orderModeFromStrategy(test.strategy)
			if test.mutate != nil {
				test.mutate(&input)
			}
			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, test.want, result.RouteID)
		})
	}
}

func TestV4KeyThresholdIsDerivedFromSharedEvidence(t *testing.T) {
	input := basePlanInput()
	input.Quality.Routes["route-cheap"] = QualityState{
		Phase: QualityOpen, Epoch: 7, ConsecutiveHardFailures: 2,
		FailureWindowUntilMS: input.NowMS + 60_000, NextProbeAtMS: input.NowMS + 30_000,
		ReliabilityPPM: 600_000, ReliabilitySamples: 10,
	}

	tolerant := input
	tolerant.Policy.FailureThreshold = 3
	result, err := Plan(tolerant)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.False(t, result.Candidates[0].KeySuppressed)

	sensitive := input
	sensitive.Policy.FailureThreshold = 2
	result, err = Plan(sensitive)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectKeySuppressed})

	expired := input
	expired.Policy.FailureThreshold = 1
	expired.NowMS = input.Quality.Routes["route-cheap"].FailureWindowUntilMS + 1
	result, err = Plan(expired)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
}

func TestV4BackgroundRecoveryStillHonorsPerKeyOpenThreshold(t *testing.T) {
	input := basePlanInput()
	input.BackgroundRecovery = true
	input.Quality.Routes["route-cheap"] = QualityState{
		Phase: QualityOpen, Epoch: 7, ConsecutiveHardFailures: 2,
		FailureWindowUntilMS: input.NowMS + 60_000, NextProbeAtMS: input.NowMS + 30_000,
	}

	input.Policy.FailureThreshold = 3
	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionDegraded, result.Admission)

	input.Policy.FailureThreshold = 2
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectKeySuppressed})
}

func TestV4CredentialDomainBlocksEveryRouteUsingTheCredential(t *testing.T) {
	input := basePlanInput()
	routes := input.Catalog.Contracts[testContractID].Pools[testPoolID].Candidates
	for index := range routes {
		routes[index].CredentialDomain = "credential:shared"
	}
	contract := input.Catalog.Contracts[testContractID]
	contract.Pools[testPoolID] = CertifiedPool{Candidates: routes}
	input.Catalog.Contracts[testContractID] = contract
	input.Credentials = CredentialSnapshot{Domains: map[string]CredentialState{
		"credential:shared": {Epoch: 1, BlockedUntilMS: input.NowMS + 10_000},
	}}

	result, err := Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
	assert.Len(t, result.Rejections, 2)
	for _, rejection := range result.Rejections {
		assert.Equal(t, RejectCredentialBlocked, rejection.Reason)
	}
}

func TestV4SingleRouteModelRemainsUsable(t *testing.T) {
	input := basePlanInput()
	route := testRoute("only-route", "cheap", 201, "cache-only", "capacity-only", false)
	input.Catalog = testCatalog(route)
	input.Prices = PriceSnapshot{Version: "price-single", RatiosPPM: map[string]int64{"only-route": 20_000}}
	input.Quality = QualitySnapshot{Routes: map[string]QualityState{
		"only-route": {Phase: QualityHealthy, Epoch: 1, ReliabilityPPM: 900_000, ReliabilitySamples: 10},
	}}
	input.AllowedGroups = []string{"cheap"}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "only-route", result.RouteID)
}

func TestV4VirginRouteBecomesHealthyAfterFirstSuccessfulRequest(t *testing.T) {
	config := DefaultQualityConfig()
	state := InitialQuality(false)

	state = ReduceQuality(state, QualityEvent{AtMS: 1, Outcome: OutcomeSuccess}, config)
	assert.Equal(t, QualityHealthy, state.Phase)

	// A second request must remain normally eligible; it must not enter a
	// hidden warm-up lane merely because the route has no administrator backup.
	state = ReduceQuality(state, QualityEvent{AtMS: 2, Outcome: OutcomeSuccess}, config)
	assert.Equal(t, QualityHealthy, state.Phase)

	failed := ReduceQuality(InitialQuality(false), QualityEvent{
		AtMS: 1, Outcome: OutcomeInfrastructure, HTTPStatus: 503,
	}, config)
	assert.NotEqual(t, QualityHealthy, failed.Phase)
}

func TestV3PolicyMapsIntoV4RuntimeAndV4MayInheritPriceLimit(t *testing.T) {
	legacy := testPolicy()
	legacy.Version = LegacyPolicyVersion
	legacy.Strategy = ""
	legacy.MaxAttempts = 0
	legacy.FailureThreshold = 0
	legacy = NormalizePolicy(legacy)
	require.NoError(t, legacy.Validate())
	assert.Equal(t, StrategyPrice, legacy.EffectiveStrategy())
	assert.Equal(t, DefaultPolicyMaxAttempts, legacy.EffectiveMaxAttempts())
	assert.Equal(t, DefaultPolicyFailureThreshold, legacy.EffectiveFailureThreshold())

	v4 := testPolicy()
	v4.PolicyID = ""
	v4.PoolPolicy = ""
	v4.MaxEffectiveRatioPPM = 0
	require.NoError(t, NormalizePolicy(v4).Validate())
}
