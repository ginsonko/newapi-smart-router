package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func recoveryFallbackInput() PlanInput {
	input := basePlanInput()
	input.RecoveryFallbackEnabled = true
	input.TextRecoveryProbeAfterMS = 300_000
	input.NowMS = 500_000
	for id := range input.Quality.Routes {
		input.Quality.Routes[id] = QualityState{
			Phase: QualityOpen, Epoch: 2, DemandRecovery: true,
			RecoveryStartedMS: 1, LastFailureMS: 400_000,
			ConsecutiveHardFailures: 3, NextProbeAtMS: 900_000,
		}
	}
	return input
}

func TestRecoveryFallbackStagedTextUsesNormalThenRankedRemainingChannels(t *testing.T) {
	input := recoveryFallbackInput()
	input.Quality.Routes["route-plus"] = QualityState{Phase: QualityHealthy}
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-plus", plan.RouteID)
	require.False(t, plan.RecoveryFallback, "healthy route wins even when more expensive")
	input.Attempt.StartedAttempts = 1
	input.Attempt.AttemptedChannels = []int{102}
	input.Attempt.AttemptedRoutes = []string{"route-plus"}
	plan, err = Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-cheap", plan.RouteID)
	require.True(t, plan.RecoveryFallback)
	require.Equal(t, AdmissionLastResort, plan.Admission)
	input.Attempt.AttemptedChannels = append(input.Attempt.AttemptedChannels, 101)
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestRecoveryFallbackAutomaticExclusionsAndCredentialCache(t *testing.T) {
	input := recoveryFallbackInput()
	input.AutomaticExcludedRoutes = []string{"route-cheap", "route-plus"}
	route := input.Catalog.Contracts[testContractID].Pools[testPoolID].Candidates[0]
	input.Credentials.Domains = map[string]CredentialState{
		RouteCredentialDomain(route): {BlockedUntilMS: input.NowMS + 60_000},
	}
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-cheap", plan.RouteID)
	require.True(t, plan.RecoveryFallback)
	input.RecoveryFallbackEnabled = false
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestRecoveryFallbackRetainsScopePriceAndCompatibility(t *testing.T) {
	for _, name := range []string{"group_excluded", "route_excluded", "unauthorized", "price_limit", "missing_price", "context", "capability", "committed"} {
		t.Run(name, func(t *testing.T) {
			input := recoveryFallbackInput()
			input.Attempt.AttemptedChannels = []int{102}
			switch name {
			case "group_excluded":
				input.Policy.ExcludedGroups = []string{"cheap"}
			case "route_excluded":
				input.Policy.ExcludedRoutes = []string{"route-cheap"}
			case "unauthorized":
				input.AllowedGroups = []string{"plus"}
			case "price_limit":
				input.Policy.MaxEffectiveRatioPPM = 10_000
			case "missing_price":
				delete(input.Prices.RatiosPPM, "route-cheap")
			case "context":
				input.Request.EstimatedContext = 128_001
			case "capability":
				input.Request.RequiredCapabilities = 1 << 30
			case "committed":
				input.Attempt.Committed = true
			}
			_, err := Plan(input)
			require.Error(t, err)
		})
	}
}

func TestRecoveryFallbackRechecksCapacityBackoffWithoutExceedingInflight(t *testing.T) {
	input := recoveryFallbackInput()
	input.Capacity.Domains = make(map[string]CapacityState)
	input.Capacity.Domains["cap-cheap"] = CapacityState{Known: true, MaxInflight: 1, Inflight: 1}
	input.Capacity.Domains["cap-plus"] = CapacityState{Known: true, MaxInflight: 2, Inflight: 1, BlockedUntilMS: input.NowMS + 60_000}
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-plus", plan.RouteID)
	require.True(t, plan.RecoveryFallback)
	input.Capacity.Domains["cap-plus"] = CapacityState{Known: true, MaxInflight: 2, Inflight: 2}
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrCapacityConstrained)
}

func TestRecoveryFallbackRunsAfterNormalCapacitySelectionFails(t *testing.T) {
	input := recoveryFallbackInput()
	input.Quality.Routes["route-cheap"] = QualityState{Phase: QualityHealthy}
	input.Capacity.Domains = map[string]CapacityState{
		"cap-cheap": {Known: true, MaxInflight: 1, Inflight: 1},
	}
	plan, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-plus", plan.RouteID)
	require.True(t, plan.RecoveryFallback)
}

func TestRecoveryFallbackMediaRequiresKnownNonAcceptanceForSecondSubmission(t *testing.T) {
	for _, replay := range []ReplayClass{ReplaySafeImage, ReplaySafeVideo, ReplaySafeAudio} {
		t.Run(replay.String(), func(t *testing.T) {
			input := recoveryFallbackInput()
			input.Request.ReplayClass = replay
			if replay == ReplaySafeImage {
				for routeID := range input.Quality.Routes {
					input.Quality.Routes[routeID] = imageQualityFailures(11, input.NowMS)
				}
			}
			plan, err := Plan(input)
			require.NoError(t, err)
			if replay == ReplaySafeImage {
				require.Equal(t, AdmissionLastResort, plan.Admission)
			} else {
				require.True(t, plan.RecoveryFallback)
			}
			input.Attempt.StartedAttempts = 1
			input.Attempt.AttemptedChannels = []int{plan.ChannelID}
			input.Attempt.RecoveryProbeUsed = true
			_, err = Plan(input)
			require.Error(t, err)
			input.Attempt.VirginMediaRetryAuthorized = true
			plan, err = Plan(input)
			require.NoError(t, err)
			if replay == ReplaySafeImage {
				require.Equal(t, AdmissionLastResort, plan.Admission)
			} else {
				require.True(t, plan.RecoveryFallback)
			}
			input.Attempt.Committed = true
			_, err = Plan(input)
			require.ErrorIs(t, err, ErrAlreadyCommitted)
		})
	}
}
