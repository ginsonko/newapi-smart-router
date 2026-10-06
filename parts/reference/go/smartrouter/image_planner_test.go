package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func imagePlanInput() PlanInput {
	input := basePlanInput()
	input.Request.ReplayClass = ReplaySafeImage
	input.BackgroundRecovery = true
	input.TextRecoveryProbeAfterMS = 300_000
	input.RecoveryFallbackEnabled = true
	input.Attempt.RecoveryProbeUsed = true
	return input
}

func TestImagePlannerAllUnavailableKeepsOrderAndAttemptsEachChannelOnce(t *testing.T) {
	input := imagePlanInput()
	for routeID := range input.Quality.Routes {
		input.Quality.Routes[routeID] = imageQualityFailures(11, input.NowMS)
	}
	plan, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", plan.RouteID)
	assert.Equal(t, AdmissionLastResort, plan.Admission)
	require.Len(t, plan.Candidates, 2)
	assert.Equal(t, "route-plus", plan.Candidates[1].Route.RouteID)
	input.Attempt.AttemptedChannels = []int{101}
	input.Attempt.StartedAttempts = 1
	input.Attempt.VirginMediaRetryAuthorized = true
	plan, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", plan.RouteID)
	assert.Equal(t, AdmissionLastResort, plan.Admission)
	input.Attempt.AttemptedChannels = []int{101, 102}
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestImagePlannerAvailableRoutesAvoidLegacyRecoveryGates(t *testing.T) {
	for _, phase := range []QualityPhase{QualityUnknown, QualityBootstrap, QualityDegraded, QualityOpen, QualityHalfOpen, QualityWarming, QualityQuarantined} {
		t.Run(string(phase), func(test *testing.T) {
			input := imagePlanInput()
			input.Quality.Routes["route-cheap"] = QualityState{
				Phase: phase, ConsecutiveHardFailures: 3, DemandRecovery: true, PassiveRecovery: true, NextProbeAtMS: input.NowMS + 3600_000,
			}
			input.Quality.Routes["route-plus"] = imageQualityFailures(11, input.NowMS)
			plan, err := Plan(input)
			require.NoError(test, err)
			assert.Equal(test, "route-cheap", plan.RouteID)
			assert.Equal(test, AdmissionNormal, plan.Admission)
			require.Len(test, plan.Candidates, 1)
		})
	}
	input := imagePlanInput()
	input.Quality.Routes["route-cheap"] = imageQualityFailures(11, input.NowMS)
	plan, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", plan.RouteID)
	assert.Equal(t, AdmissionNormal, plan.Admission)
	assert.False(t, IsSyntheticProbeSafe(ReplaySafeImage))
}

func TestImagePlannerHealthGuardDisabledStillAvoidsUnavailable(t *testing.T) {
	input := imagePlanInput()
	input.Policy.HealthGuard = false
	input.Quality.Routes["route-cheap"] = imageQualityFailures(11, input.NowMS)
	plan, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", plan.RouteID)
	assert.Equal(t, AdmissionNormal, plan.Admission)
	input.Quality.Routes["route-plus"] = imageQualityFailures(11, input.NowMS)
	plan, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", plan.RouteID)
	assert.Equal(t, AdmissionLastResort, plan.Admission)
}

func TestImagePlannerLastResortPreservesHardFilters(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(*PlanInput)
		reason RejectReason
	}{
		{"unauthorized", func(input *PlanInput) { input.AllowedGroups = []string{"plus"} }, RejectGroupUnauthorized},
		{"group-excluded", func(input *PlanInput) { input.Policy.ExcludedGroups = []string{"cheap"} }, RejectGroupExcluded},
		{"route-excluded", func(input *PlanInput) { input.Policy.ExcludedRoutes = []string{"route-cheap"} }, RejectRouteExcluded},
		{"price-ceiling", func(input *PlanInput) { input.Prices.RatiosPPM["route-cheap"] = 50_001 }, RejectAbovePriceLimit},
		{"missing-price", func(input *PlanInput) { delete(input.Prices.RatiosPPM, "route-cheap") }, RejectPriceMissing},
		{"capability", func(input *PlanInput) {
			entry := input.Catalog.Contracts[testContractID]
			pool := entry.Pools[testPoolID]
			pool.Candidates[0].Capabilities = 0
			entry.Pools[testPoolID] = pool
			input.Catalog.Contracts[testContractID] = entry
		}, RejectCapabilityMismatch},
		{"manual-unlisted", func(input *PlanInput) {
			input.Policy.OrderMode = OrderManual
			input.Policy.AllowFutureGroups = false
			input.Policy.ManualGroupOrder = []string{"cheap", "plus"}
			input.Policy.ContractOverrides[testContractID] = ContractOverride{OrderMode: OrderManual, ManualRouteOrder: []string{"route-plus"}}
			input.Policy.AllowFutureCertifiedRoutes = false
		}, RejectUnlistedManualRoute},
		{"credential", func(input *PlanInput) {
			route := input.Catalog.Contracts[testContractID].Pools[testPoolID].Candidates[0]
			input.Credentials.Domains = map[string]CredentialState{RouteCredentialDomain(route): {BlockedUntilMS: input.NowMS + 1000}}
		}, RejectCredentialBlocked},
	}
	for _, item := range tests {
		t.Run(item.name, func(test *testing.T) {
			input := imagePlanInput()
			for routeID := range input.Quality.Routes {
				input.Quality.Routes[routeID] = imageQualityFailures(11, input.NowMS)
			}
			item.edit(&input)
			plan, err := Plan(input)
			require.NoError(test, err)
			assert.Equal(test, "route-plus", plan.RouteID)
			assert.Contains(test, plan.Rejections, Rejection{RouteID: "route-cheap", Reason: item.reason})
			input.Attempt.AttemptedChannels = []int{102}
			_, err = Plan(input)
			require.ErrorIs(test, err, ErrNoCandidate)
		})
	}
}

func TestImagePlannerManualOrderAndPhysicalAliasExclusion(t *testing.T) {
	input := imagePlanInput()
	input.Policy.OrderMode = OrderManual
	input.Policy.ContractOverrides[testContractID] = ContractOverride{OrderMode: OrderManual, ManualRouteOrder: []string{"route-plus", "route-cheap"}}
	for routeID := range input.Quality.Routes {
		input.Quality.Routes[routeID] = imageQualityFailures(11, input.NowMS)
	}
	plan, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", plan.RouteID)
	entry := input.Catalog.Contracts[testContractID]
	pool := entry.Pools[testPoolID]
	pool.Candidates[0].ChannelID = pool.Candidates[1].ChannelID
	entry.Pools[testPoolID] = pool
	input.Catalog.Contracts[testContractID] = entry
	input.Attempt.AttemptedChannels = []int{102}
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}
