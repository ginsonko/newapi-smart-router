package smartrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testContractID = "contract-text-v1"
	testPoolID     = "balanced-v1"
)

func testPolicy() Policy {
	return Policy{
		Version:              PolicyVersion,
		PolicyID:             "balanced-v1",
		Enabled:              true,
		PoolPolicy:           testPoolID,
		OrderMode:            OrderAutoPrice,
		MaxEffectiveRatioPPM: 50_000,
		ExcludedGroups:       []string{},
		ExcludedRoutes:       []string{},
		ContractOverrides:    map[string]ContractOverride{},
		HealthGuard:          true,
		AllowFutureGroups:    true,
		RecoveryProfile:      RecoveryBalanced,
		TTFTPolicy: TTFTPolicy{
			Enabled:    false,
			Metric:     TTFTMetricP95,
			TargetMS:   2_500,
			HardMaxMS:  6_000,
			MinSamples: 8,
		},
		QueuePolicy: QueuePolicy{Mode: QueueModeNoQueue},
	}
}

func testRoute(routeID, group string, channelID int, cacheDomain, capacityDomain string, stableFallback bool) CertifiedRoute {
	return CertifiedRoute{
		RouteID:           routeID,
		ChannelGeneration: "generation-v1",
		Group:             group,
		ChannelID:         channelID,
		UpstreamModel:     "upstream-gpt",
		Capabilities:      0b111,
		MaxContext:        128_000,
		CacheDomain:       cacheDomain,
		CapacityDomain:    capacityDomain,
		StableFallback:    stableFallback,
	}
}

func testCatalog(routes ...CertifiedRoute) CertifiedRouteCatalog {
	if len(routes) == 0 {
		routes = []CertifiedRoute{
			testRoute("route-cheap", "cheap", 101, "cache-cheap", "cap-cheap", false),
			testRoute("route-plus", "plus", 102, "cache-plus", "cap-plus", true),
		}
	}
	return CertifiedRouteCatalog{
		Version: "catalog-v1",
		Contracts: map[string]ContractCatalog{
			testContractID: {
				Contract: RouteContract{
					ContractID:            testContractID,
					CanonicalModel:        "gpt-test",
					Endpoint:              "responses:stream",
					CapabilityFingerprint: "tools+stream",
				},
				Pools: map[string]CertifiedPool{
					testPoolID: {Candidates: routes},
				},
			},
		},
	}
}

func basePlanInput() PlanInput {
	catalog := testCatalog()
	return PlanInput{
		NowMS:   100_000,
		Policy:  testPolicy(),
		Catalog: catalog,
		Prices: PriceSnapshot{
			Version: "price-v1",
			RatiosPPM: map[string]int64{
				"route-cheap": 20_000,
				"route-plus":  50_000,
			},
		},
		Quality: QualitySnapshot{
			Version: "quality-v1",
			Routes: map[string]QualityState{
				"route-cheap": {Phase: QualityHealthy, StableSinceMS: 1},
				"route-plus":  {Phase: QualityHealthy, StableSinceMS: 1},
			},
		},
		Request: RouteRequest{
			ContractID:            testContractID,
			CanonicalModel:        "gpt-test",
			Endpoint:              "responses:stream",
			CapabilityFingerprint: "tools+stream",
			RequiredCapabilities:  0b011,
			EstimatedContext:      10_000,
			ReplayClass:           ReplaySafeText,
			WarmingKey:            "session-1",
			DeadlineMS:            110_000,
		},
		AllowedGroups: []string{"cheap", "plus"},
		Attempt:       AttemptState{MaxAttempts: 3},
	}
}

func TestCatalogJSONRoundTripPreservesContractPoolAndExactRoutes(t *testing.T) {
	catalog := testCatalog()
	require.NoError(t, catalog.Validate())

	data, err := json.Marshal(catalog)
	require.NoError(t, err)
	var decoded CertifiedRouteCatalog
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NoError(t, decoded.Validate())

	pool := decoded.Contracts[testContractID].Pools[testPoolID]
	require.Len(t, pool.Candidates, 2)
	assert.Equal(t, "route-cheap", pool.Candidates[0].RouteID)
	assert.Equal(t, 101, pool.Candidates[0].ChannelID)
	assert.Equal(t, "route-plus", pool.Candidates[1].RouteID)
	assert.Equal(t, 102, pool.Candidates[1].ChannelID)
}

func TestPlanFailsClosedWithoutPriceSnapshotOrRoutePrice(t *testing.T) {
	t.Run("snapshot missing", func(t *testing.T) {
		input := basePlanInput()
		input.Prices = PriceSnapshot{}
		_, err := Plan(input)
		require.ErrorIs(t, err, ErrPriceUnavailable)
	})

	t.Run("one route price missing", func(t *testing.T) {
		input := basePlanInput()
		delete(input.Prices.RatiosPPM, "route-cheap")
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectPriceMissing})
	})
}

func TestPlanUsesFixedPointPriceAndDeterministicCatalogTieBreak(t *testing.T) {
	input := basePlanInput()
	input.Prices.RatiosPPM["route-cheap"] = 50_000
	input.Prices.RatiosPPM["route-plus"] = 50_000

	for attempt := 0; attempt < 5; attempt++ {
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, 101, result.ChannelID)
	}
}

func TestPlanEnforcesExclusionsAuthorizationAndMaximumRatio(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PlanInput)
		reason RejectReason
	}{
		{
			name: "group exclusion",
			mutate: func(input *PlanInput) {
				input.Policy.ExcludedGroups = []string{"cheap"}
			},
			reason: RejectGroupExcluded,
		},
		{
			name: "route exclusion",
			mutate: func(input *PlanInput) {
				input.Policy.ExcludedRoutes = []string{"route-cheap"}
			},
			reason: RejectRouteExcluded,
		},
		{
			name: "group unauthorized",
			mutate: func(input *PlanInput) {
				input.AllowedGroups = []string{"plus"}
			},
			reason: RejectGroupUnauthorized,
		},
		{
			name: "above maximum",
			mutate: func(input *PlanInput) {
				input.Policy.MaxEffectiveRatioPPM = 10_000
			},
			reason: RejectAbovePriceLimit,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := basePlanInput()
			test.mutate(&input)
			result, err := Plan(input)
			if test.reason == RejectAbovePriceLimit {
				require.ErrorIs(t, err, ErrNoCandidate)
				assert.Len(t, result.Rejections, 2)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "route-plus", result.RouteID)
			assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: test.reason})
		})
	}
}

func TestPlanRejectsReplayAndContractCapabilityMismatch(t *testing.T) {
	t.Run("zero replay class fails closed", func(t *testing.T) {
		input := basePlanInput()
		input.Request.ReplayClass = ReplayUnsupported
		_, err := Plan(input)
		require.ErrorIs(t, err, ErrUnsupportedReplay)
	})

	for _, replay := range []ReplayClass{ReplaySafeImage, ReplaySafeVideo} {
		t.Run(replay.String()+" is routable", func(t *testing.T) {
			input := basePlanInput()
			input.Request.ReplayClass = replay
			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, "route-cheap", result.RouteID)
		})
	}

	t.Run("contract fingerprint mismatch", func(t *testing.T) {
		input := basePlanInput()
		input.Request.CapabilityFingerprint = "vision"
		_, err := Plan(input)
		require.ErrorIs(t, err, ErrContractMismatch)
	})

	t.Run("route capability mismatch", func(t *testing.T) {
		input := basePlanInput()
		input.Request.RequiredCapabilities = 0b1000
		result, err := Plan(input)
		require.ErrorIs(t, err, ErrNoCandidate)
		assert.Len(t, result.Rejections, 2)
		assert.Equal(t, RejectCapabilityMismatch, result.Rejections[0].Reason)
	})
}

func TestPlanNeverRetriesExactFailedRouteAndUsesSingleAttemptBudget(t *testing.T) {
	input := basePlanInput()
	input.Attempt.AttemptedRoutes = []string{"route-cheap"}
	input.Attempt.StartedAttempts = 1

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectAlreadyAttempted})

	input.Attempt.StartedAttempts = input.Attempt.MaxAttempts
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrAttemptBudget)

	input.Attempt.StartedAttempts = 1
	input.Attempt.Committed = true
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrAlreadyCommitted)
}

func TestPlanNeverRetriesSamePhysicalChannelThroughAnotherRouteID(t *testing.T) {
	input := basePlanInput()
	contract := input.Catalog.Contracts[testContractID]
	pool := contract.Pools[testPoolID]
	duplicate := testRoute("route-cheap-overlap", "cheap", 101, "cache-cheap", "cap-cheap", false)
	pool.Candidates = append([]CertifiedRoute{duplicate}, pool.Candidates...)
	contract.Pools[testPoolID] = pool
	input.Catalog.Contracts[testContractID] = contract
	input.Prices.RatiosPPM[duplicate.RouteID] = 10_000
	input.Attempt.AttemptedChannels = []int{101}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectChannelAttempted})
	assert.Contains(t, result.Rejections, Rejection{RouteID: duplicate.RouteID, Reason: RejectChannelAttempted})
}

func TestStrongAffinityWinsPriceButCapacityOverflowPreservesAffinity(t *testing.T) {
	t.Run("healthy affinity wins", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.AffinityMaxPremiumPercent = 200
		input.Affinity = &AffinityState{
			Strength:    AffinityStrong,
			ContractID:  testContractID,
			RouteID:     "route-plus",
			ChannelID:   102,
			CacheDomain: "cache-plus",
		}

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Equal(t, AffinityPreserve, result.AffinityDisposition)
		assert.False(t, result.CapacityOverflow)
	})

	t.Run("temporary overflow keeps original affinity", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.AffinityMaxPremiumPercent = 200
		input.Affinity = &AffinityState{
			Strength:    AffinityStrong,
			ContractID:  testContractID,
			RouteID:     "route-plus",
			ChannelID:   102,
			CacheDomain: "cache-plus",
		}
		input.Capacity = CapacitySnapshot{Domains: map[string]CapacityState{
			"cap-plus": {Known: true, MaxInflight: 1, Inflight: 1},
		}}

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AffinityPreserve, result.AffinityDisposition)
		assert.True(t, result.CapacityOverflow)
	})
}

func TestHealthGuardHandlesBootstrapHalfOpenWarmingAndDegraded(t *testing.T) {
	t.Run("stable fallback bootstraps while half-open route is excluded", func(t *testing.T) {
		input := basePlanInput()
		input.Quality.Routes = map[string]QualityState{
			"route-cheap": {Phase: QualityHalfOpen},
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Equal(t, AdmissionBootstrap, result.Admission)
		assert.Equal(t, 1, result.MaxInflightHint)
	})

	t.Run("warming route admits deterministic canary", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.RecoveryProfile = RecoveryFast
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase:             QualityWarming,
			WarmingTrafficPPM: 1_000_000,
			WarmingEpoch:      3,
			SampleDueAtMS:     200_000,
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionWarming, result.Admission)
	})

	t.Run("ordinary request without cache key uses independent admission key", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.RecoveryProfile = RecoveryFast
		input.Request.WarmingKey = ""
		input.Request.AdmissionKey = "request-42"
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase:             QualityWarming,
			WarmingTrafficPPM: 1_000_000,
			WarmingEpoch:      1,
			SampleDueAtMS:     input.NowMS + 30_000,
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionWarming, result.Admission)
	})

	t.Run("degraded cheap route remains usable below this key threshold", func(t *testing.T) {
		input := basePlanInput()
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase: QualityDegraded, SampleDueAtMS: input.NowMS + 1,
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionDegraded, result.Admission)
	})

	t.Run("degraded cheap route gets one due recovery sample", func(t *testing.T) {
		input := basePlanInput()
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase: QualityDegraded, SampleDueAtMS: input.NowMS,
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionWarmSample, result.Admission)
		assert.Equal(t, 1, result.MaxInflightHint)
	})

	t.Run("suppressed key does not immediately re-dispatch first degraded failure", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.FailureThreshold = 1
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase:                   QualityDegraded,
			ConsecutiveHardFailures: 1,
			SampleDueAtMS:           input.NowMS + 30_000,
		}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectKeySuppressed})
		assert.NotEqual(t, AdmissionProbe, result.Admission)

		input.NowMS += 30_000
		result, err = Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionProbe, result.Admission)
	})

	t.Run("legacy open evidence remains usable below this key threshold", func(t *testing.T) {
		input := basePlanInput()
		input.Quality.Routes["route-cheap"] = QualityState{Phase: QualityOpen, NextProbeAtMS: input.NowMS + 1}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionDegraded, result.Admission)
	})
}

func TestPriceStrategyKeepsRecoverableCheapRouteInOrderWithBackgroundProbes(t *testing.T) {
	input := basePlanInput()
	input.BackgroundRecovery = true
	input.Quality.Routes["route-cheap"] = QualityState{
		Phase: QualityDegraded, ConsecutiveHardFailures: 1,
		SampleDueAtMS: input.NowMS + 30_000,
	}
	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionDegraded, result.Admission)

	input.Quality.Routes["route-cheap"] = QualityState{
		Phase: QualityDegraded, ConsecutiveHardFailures: 3,
		FailureWindowUntilMS: input.NowMS + 30_000,
		SampleDueAtMS:        input.NowMS + 30_000,
	}
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectKeySuppressed})
}

func TestBackgroundRecoveryDoesNotReturnKnownBadRoutesToForeground(t *testing.T) {
	for _, phase := range []QualityPhase{QualityUnknown, QualityOpen, QualityHalfOpen} {
		t.Run(string(phase), func(t *testing.T) {
			input := basePlanInput()
			input.BackgroundRecovery = true
			input.Quality.Routes["route-cheap"] = QualityState{
				Phase: phase, ConsecutiveHardFailures: 3,
				FailureWindowUntilMS: input.NowMS + 30_000,
				NextProbeAtMS:        input.NowMS + 30_000,
			}

			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, "route-plus", result.RouteID)
			assert.NotEqual(t, AdmissionLastResort, result.Admission)
			reason := RejectHealthUnavailable
			if phase == QualityOpen {
				reason = RejectKeySuppressed
			}
			assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: reason})

			input.Policy.MaxEffectiveRatioPPM = 20_000
			_, err = Plan(input)
			require.ErrorIs(t, err, ErrNoCandidate)
		})
	}
}

func TestBackgroundRecoveryExposesVirginUnknownsButAttemptsOnlyOnePerRequestChain(t *testing.T) {
	input := basePlanInput()
	input.BackgroundRecovery = true
	input.Quality.Routes = map[string]QualityState{
		"route-cheap": InitialQuality(false),
		"route-plus":  InitialQuality(false),
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
	assert.Equal(t, 1, result.MaxInflightHint)
	assert.Len(t, result.Candidates, 2)

	input.Attempt.RecoveryProbeUsed = true
	input.Attempt.StartedAttempts = 1
	input.Attempt.AttemptedRoutes = []string{"route-cheap"}
	input.Attempt.AttemptedChannels = []int{101}
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestVirginBootstrapInitialSelectionIsSingleFlightForTextImageAndVideoRequests(t *testing.T) {
	for _, test := range []struct {
		name               string
		replayClass        ReplayClass
		backgroundRecovery bool
		wantCandidates     int
	}{
		{name: "text", replayClass: ReplaySafeText, backgroundRecovery: true, wantCandidates: 2},
		{name: "image", replayClass: ReplaySafeImage, wantCandidates: 1},
		{name: "video", replayClass: ReplaySafeVideo, wantCandidates: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := basePlanInput()
			input.Request.ReplayClass = test.replayClass
			input.BackgroundRecovery = test.backgroundRecovery
			input.Quality.Routes = map[string]QualityState{
				"route-cheap": InitialQuality(false),
				"route-plus":  InitialQuality(false),
			}

			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
			assert.Equal(t, 1, result.MaxInflightHint)
			assert.Len(t, result.Candidates, test.wantCandidates)
		})
	}
}

func TestOnlyExplicitlyAuthorizedMediaMayAdvanceToNextVirginRoute(t *testing.T) {
	for _, test := range []struct {
		name        string
		replayClass ReplayClass
		authorized  bool
		wantRoute   string
		wantErr     error
	}{
		{name: "image authorized", replayClass: ReplaySafeImage, authorized: true, wantRoute: "route-plus"},
		{name: "video authorized", replayClass: ReplaySafeVideo, authorized: true, wantRoute: "route-plus"},
		{name: "image not authorized", replayClass: ReplaySafeImage, wantErr: ErrNoCandidate},
		{name: "video not authorized", replayClass: ReplaySafeVideo, wantErr: ErrNoCandidate},
		{name: "text cannot use media authorization", replayClass: ReplaySafeText, authorized: true, wantErr: ErrNoCandidate},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := basePlanInput()
			input.Request.ReplayClass = test.replayClass
			input.Quality.Routes = map[string]QualityState{
				"route-cheap": InitialQuality(false),
				"route-plus":  InitialQuality(false),
			}
			input.Attempt.StartedAttempts = 1
			input.Attempt.AttemptedRoutes = []string{"route-cheap"}
			input.Attempt.AttemptedChannels = []int{101}
			input.Attempt.RecoveryProbeUsed = true
			input.Attempt.VirginMediaRetryAuthorized = test.authorized

			result, err := Plan(input)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantRoute, result.RouteID)
			assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
			assert.Len(t, result.Candidates, 1)
		})
	}
}

func TestBackgroundPriceExploresCheaperVirginBeforeHealthyRoute(t *testing.T) {
	input := basePlanInput()
	input.BackgroundRecovery = true
	input.Quality.Routes["route-cheap"] = InitialQuality(false)

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
	assert.Len(t, result.Candidates, 2)
}

func TestStableFallbackRemainsAvailableAfterColdStartSuccessWithoutWarmingKey(t *testing.T) {
	input := basePlanInput()
	input.Request.WarmingKey = ""
	input.Quality.Routes = map[string]QualityState{
		"route-cheap": {Phase: QualityHalfOpen},
	}

	bootstrap, err := Plan(input)
	require.NoError(t, err)
	require.Equal(t, "route-plus", bootstrap.RouteID)
	require.Equal(t, AdmissionBootstrap, bootstrap.Admission)

	input.Quality.Routes["route-plus"] = ReduceQuality(
		InitialQuality(true),
		QualityEvent{AtMS: input.NowMS, Outcome: OutcomeSuccess},
		DefaultQualityConfig(),
	)
	afterSuccess, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", afterSuccess.RouteID)
	assert.Equal(t, AdmissionNormal, afterSuccess.Admission)
}

func TestRecoveryProfilesGateRecoveredRoutesForNewSessions(t *testing.T) {
	tests := []struct {
		name    string
		profile RecoveryProfile
		delayMS int64
	}{
		{name: "fast", profile: RecoveryFast, delayMS: 0},
		{name: "balanced", profile: RecoveryBalanced, delayMS: 5 * 60 * 1_000},
		{name: "stable", profile: RecoveryStable, delayMS: 30 * 60 * 1_000},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := basePlanInput()
			input.Policy.Strategy = StrategyBalanced
			input.Policy.RecoveryProfile = test.profile
			input.Quality.Routes["route-cheap"] = QualityState{
				Phase:         QualityHealthy,
				WarmingEpoch:  1,
				StableSinceMS: 10_000,
			}
			if test.delayMS > 0 {
				input.NowMS = 10_000 + test.delayMS - 1
				result, err := Plan(input)
				require.NoError(t, err)
				assert.Equal(t, "route-plus", result.RouteID)
				assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectRecoveryCooling})
			}
			input.NowMS = 10_000 + test.delayMS
			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, "route-cheap", result.RouteID)
		})
	}
}

func TestHealthyStrongAffinitySurvivesRecoveryObservationWindow(t *testing.T) {
	input := basePlanInput()
	input.Policy.AffinityMaxPremiumPercent = 200
	input.Quality.Routes["route-plus"] = QualityState{
		Phase:         QualityHealthy,
		WarmingEpoch:  1,
		StableSinceMS: input.NowMS - 1_000,
	}
	input.Affinity = &AffinityState{
		Strength:    AffinityStrong,
		ContractID:  testContractID,
		RouteID:     "route-plus",
		ChannelID:   102,
		CacheDomain: "cache-plus",
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Equal(t, AffinityPreserve, result.AffinityDisposition)
}

func TestStrongAffinityCannotExceedConfiguredPricePremium(t *testing.T) {
	input := basePlanInput()
	input.Policy.AffinityMaxPremiumPercent = 10
	input.Affinity = &AffinityState{
		Strength: AffinityStrong, ContractID: testContractID, RouteID: "route-plus", ChannelID: 102,
		CacheDomain: "cache-plus",
	}
	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AffinityReplaceOnSuccess, result.AffinityDisposition)
}

func TestEconomicAffinityUsesSnapshotWithoutChangingFixedMode(t *testing.T) {
	input := basePlanInput()
	input.Request.EstimatedContext = 110_000
	input.Policy.AffinityMaxPremiumPercent = 0
	input.Affinity = &AffinityState{
		Strength: AffinityStrong, ContractID: testContractID,
		RouteID: "route-plus", ChannelID: 102, CacheDomain: "cache-plus",
	}
	input.CacheEconomy = &CacheEconomySnapshot{
		FeatureVersion: CacheEconomyFeatureVersion, ModelVersion: CacheEconomyChampionVersion,
		Samples: 30, MinimumSamples: 8, ConfidencePPM: 950_000, MinimumConfidencePPM: 700_000,
		PredictionErrorPPM: 10_000, WarmCostPerMillionContext: 100_000,
		ColdCostPerMillionContext: 10_000_000, ContextGrowthTokens: 10_000,
		CompressionThresholdTokens: 127_000,
		JointSurvivalPPM:           []int64{1_000_000, 500_000},
	}

	fixed, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", fixed.RouteID)
	assert.Equal(t, CacheEconomyReasonDisabled, fixed.CacheEconomy.Reason)

	input.Policy.AffinityMode = AffinityModeEconomicBreakEven
	economic, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", economic.RouteID)
	assert.Equal(t, CacheEconomyKeep, economic.CacheEconomy.Action)
	assert.Equal(t, AffinityPreserve, economic.AffinityDisposition)
}

func TestEconomicAffinitySwitchesWhenSavingsCoverColdStart(t *testing.T) {
	input := basePlanInput()
	input.Request.EstimatedContext = 50_000
	input.Policy.AffinityMode = AffinityModeEconomicBreakEven
	input.Policy.AffinityMaxPremiumPercent = 1000
	input.Affinity = &AffinityState{
		Strength: AffinityStrong, ContractID: testContractID,
		RouteID: "route-plus", ChannelID: 102, CacheDomain: "cache-plus",
	}
	input.CacheEconomy = &CacheEconomySnapshot{
		FeatureVersion: CacheEconomyFeatureVersion, ModelVersion: CacheEconomyChampionVersion,
		Samples: 30, MinimumSamples: 8, ConfidencePPM: 950_000, MinimumConfidencePPM: 700_000,
		PredictionErrorPPM: 10_000, WarmCostPerMillionContext: 500_000,
		ColdCostPerMillionContext: 600_000, ContextGrowthTokens: 10_000,
		CompressionThresholdTokens: 500_000,
		JointSurvivalPPM:           []int64{1_000_000, 900_000, 800_000, 700_000},
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, CacheEconomySwitch, result.CacheEconomy.Action)
	assert.Equal(t, AffinityReplaceOnSuccess, result.AffinityDisposition)
}

func TestWarmingSampleLaneRemainsAvailableDuringRecoveryCooldown(t *testing.T) {
	input := basePlanInput()
	input.Quality.Routes["route-cheap"] = QualityState{
		Phase:             QualityWarming,
		WarmingEpoch:      1,
		StableSinceMS:     input.NowMS - 1_000,
		WarmingTrafficPPM: 0,
		SampleDueAtMS:     input.NowMS,
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionWarmSample, result.Admission)
	assert.Equal(t, 1, result.MaxInflightHint)
}

func TestBackgroundRecoveryUsesRealWarmingCanaryInsteadOfWarmSample(t *testing.T) {
	t.Run("admitted request becomes a real canary even at failure threshold one", func(t *testing.T) {
		input := basePlanInput()
		input.BackgroundRecovery = true
		input.Policy.RecoveryProfile = RecoveryFast
		input.Policy.FailureThreshold = 1
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase:             QualityWarming,
			WarmingEpoch:      1,
			WarmingTrafficPPM: 1_000_000,
			SampleDueAtMS:     input.NowMS,
		}

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionWarming, result.Admission)
		assert.NotEqual(t, AdmissionWarmSample, result.Admission)
	})

	t.Run("sample deadline does not bypass the real traffic percentage", func(t *testing.T) {
		input := basePlanInput()
		input.BackgroundRecovery = true
		input.Policy.RecoveryProfile = RecoveryFast
		input.Quality.Routes["route-cheap"] = QualityState{
			Phase:             QualityWarming,
			WarmingEpoch:      1,
			WarmingTrafficPPM: 0,
			SampleDueAtMS:     input.NowMS,
		}

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectHealthUnavailable})
	})
}

func TestFreshRecoveryEvidenceAdmissionRespectsStrategy(t *testing.T) {
	const evidenceTTLMS = int64(60_000)
	base := basePlanInput()
	base.BackgroundRecovery = true
	base.RecoveryEvidenceTTLMS = evidenceTTLMS
	base.Policy.RecoveryProfile = RecoveryStable
	cheapQuality := QualityState{
		Phase:              QualityWarming,
		WarmingEpoch:       2,
		WarmingTrafficPPM:  0,
		StableSinceMS:      base.NowMS - 1_000,
		LastFailureMS:      base.NowMS - 5_000,
		LastSuccessMS:      base.NowMS - 1_000,
		ReliabilityPPM:     100_000,
		ReliabilitySamples: 100,
	}
	plusQuality := QualityState{
		Phase:              QualityHealthy,
		StableSinceMS:      1,
		ReliabilityPPM:     990_000,
		ReliabilitySamples: 100,
	}
	base.Quality.Routes = map[string]QualityState{
		"route-cheap": cheapQuality,
		"route-plus":  plusQuality,
	}

	t.Run("price immediately admits a cheaper route after fresh probe success", func(t *testing.T) {
		input := base
		input.Quality.Routes = map[string]QualityState{
			"route-cheap": cheapQuality,
			"route-plus":  plusQuality,
		}
		input.Policy.Strategy = StrategyPrice
		input.Policy.OrderMode = OrderAutoPrice

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionWarming, result.Admission)
	})

	t.Run("stability retains reliability preference", func(t *testing.T) {
		input := base
		input.Quality.Routes = map[string]QualityState{
			"route-cheap": cheapQuality,
			"route-plus":  plusQuality,
		}
		input.Policy.Strategy = StrategyStability
		input.Policy.OrderMode = OrderAutoPrice
		input.Policy.RecoveryProfile = RecoveryFast
		cheap := input.Quality.Routes["route-cheap"]
		cheap.WarmingTrafficPPM = 1_000_000
		input.Quality.Routes["route-cheap"] = cheap

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
	})

	for _, strategy := range []Strategy{StrategyLatency, StrategyManual} {
		t.Run(string(strategy)+" admits fresh recovery without canary sampling", func(t *testing.T) {
			input := base
			input.Quality.Routes = map[string]QualityState{
				"route-cheap": cheapQuality,
				"route-plus":  plusQuality,
			}
			input.Policy.Strategy = strategy
			input.Policy.OrderMode = orderModeFromStrategy(strategy)
			if strategy == StrategyManual {
				input.Policy.ManualGroupOrder = []string{"cheap", "plus"}
			}

			result, err := Plan(input)
			require.NoError(t, err)
			var cheapCandidate *Candidate
			for index := range result.Candidates {
				if result.Candidates[index].Route.RouteID == "route-cheap" {
					cheapCandidate = &result.Candidates[index]
					break
				}
			}
			require.NotNil(t, cheapCandidate)
			assert.Equal(t, AdmissionWarming, cheapCandidate.Admission)
		})
	}

	t.Run("expired success evidence cannot bypass warming admission", func(t *testing.T) {
		input := base
		input.Quality.Routes = map[string]QualityState{
			"route-cheap": cheapQuality,
			"route-plus":  plusQuality,
		}
		input.Policy.Strategy = StrategyPrice
		input.Policy.OrderMode = OrderAutoPrice
		cheap := input.Quality.Routes["route-cheap"]
		cheap.LastFailureMS = input.NowMS - evidenceTTLMS - 2_000
		cheap.LastSuccessMS = input.NowMS - evidenceTTLMS - 1_000
		input.Quality.Routes["route-cheap"] = cheap

		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Contains(t, result.Rejections, Rejection{
			RouteID: "route-cheap", Reason: RejectHealthUnavailable,
		})
	})
}

func TestBackgroundPriceExploresCheapestVirginThenFallsBackToKnownRoute(t *testing.T) {
	cheapVirgin := testRoute("route-cheap", "cheap", 101, "cache-cheap", "cap-cheap", false)
	secondVirgin := testRoute("route-middle", "middle", 103, "cache-middle", "cap-middle", false)
	known := testRoute("route-plus", "plus", 102, "cache-plus", "cap-plus", true)
	input := basePlanInput()
	input.BackgroundRecovery = true
	input.Policy.Strategy = StrategyPrice
	input.Policy.OrderMode = OrderAutoPrice
	input.Policy.MaxEffectiveRatioPPM = 50_000
	input.Catalog = testCatalog(cheapVirgin, secondVirgin, known)
	input.Prices.RatiosPPM = map[string]int64{
		"route-cheap":  20_000,
		"route-middle": 30_000,
		"route-plus":   50_000,
	}
	input.Quality.Routes = map[string]QualityState{
		"route-plus": {Phase: QualityHealthy, StableSinceMS: 1},
	}
	input.AllowedGroups = []string{"cheap", "middle", "plus"}

	first, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", first.RouteID)
	assert.Equal(t, AdmissionVirginBootstrap, first.Admission)
	require.Len(t, first.Candidates, 3)
	assert.Equal(t, "route-middle", first.Candidates[1].Route.RouteID)
	assert.Equal(t, AdmissionVirginBootstrap, first.Candidates[1].Admission)

	input.Policy.FailureThreshold = 1
	input.Attempt.StartedAttempts = 1
	input.Attempt.AttemptedRoutes = []string{"route-cheap"}
	input.Attempt.AttemptedChannels = []int{101}
	input.Attempt.RecoveryProbeUsed = true
	input.Quality.Routes["route-cheap"] = QualityState{
		Phase:                   QualityOpen,
		ConsecutiveHardFailures: 1,
		FailureWindowUntilMS:    input.NowMS + 60_000,
		LastFailureMS:           input.NowMS,
	}

	second, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", second.RouteID)
	assert.Contains(t, second.Rejections, Rejection{
		RouteID: "route-middle", Reason: RejectRecoveryBudget,
	})
}

func TestDistinctUnknownRoutesCannotFailOverWithinOneAttemptChain(t *testing.T) {
	input := basePlanInput()
	input.Quality.Routes = map[string]QualityState{
		"route-cheap": {Phase: QualityUnknown},
		"route-plus":  {Phase: QualityUnknown},
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
	assert.Len(t, result.Candidates, 1)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-plus", Reason: RejectRecoveryBudget})

	input.Attempt.StartedAttempts = 1
	input.Attempt.AttemptedRoutes = []string{"route-cheap"}
	input.Attempt.AttemptedChannels = []int{101}
	input.Attempt.RecoveryProbeUsed = true
	_, err = Plan(input)
	require.ErrorIs(t, err, ErrNoCandidate)
}

func TestTTFTDisabledAndUnknownRemainPriceNeutral(t *testing.T) {
	t.Run("latency strategy periodically promotes an unknown route for a bounded probe", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.Strategy = StrategyLatency
		input.Request.WarmingKey = ""
		input.Request.AdmissionKey = ""
		input.Quality.Routes["route-cheap"] = QualityState{Phase: QualityUnknown, Epoch: 1}
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-plus": {Samples: 100, P95MS: 500, FreshUntilMS: 200_000},
		}}
		var sampledKey string
		for index := 0; index < 10_000; index++ {
			candidate := fmt.Sprintf("request-%d", index)
			if DeterministicAdmission(candidate, "route-cheap", 1, unknownRouteProbeTrafficPPM) {
				sampledKey = candidate
				break
			}
		}
		require.NotEmpty(t, sampledKey)
		input.Request.AdmissionKey = sampledKey
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, AdmissionVirginBootstrap, result.Admission)
	})
	t.Run("price strategy never promotes a more expensive unknown route", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.Strategy = StrategyPrice
		input.Request.AdmissionKey = "request-price"
		input.Quality.Routes["route-plus"] = QualityState{Phase: QualityUnknown, Epoch: 1}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.NotEqual(t, AdmissionProbe, result.Admission)
	})

	t.Run("disabled ignores slow metric", func(t *testing.T) {
		input := basePlanInput()
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-cheap": {Samples: 100, P95MS: 20_000, FreshUntilMS: 200_000},
			"route-plus":  {Samples: 100, P95MS: 500, FreshUntilMS: 200_000},
		}}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
	})

	t.Run("unknown is neutral when enabled", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.TTFTPolicy.Enabled = true
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-plus": {Samples: 100, P95MS: 500, FreshUntilMS: 200_000},
		}}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-cheap", result.RouteID)
		assert.Equal(t, TTFTUnknown, result.Candidates[0].TTFTKnowledge)
	})

	t.Run("trusted slow route is gated", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.TTFTPolicy.Enabled = true
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-cheap": {Samples: 100, P95MS: 20_000, FreshUntilMS: 200_000},
			"route-plus":  {Samples: 100, P95MS: 500, FreshUntilMS: 200_000},
		}}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
		assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectTTFTSlow})
	})

	t.Run("known route meeting target precedes known above-target route", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.TTFTPolicy.Enabled = true
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-cheap": {Samples: 100, P95MS: 3_000, FreshUntilMS: 200_000},
			"route-plus":  {Samples: 100, P95MS: 2_000, FreshUntilMS: 200_000},
		}}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
	})

	t.Run("all trusted slow routes use lowest P95", func(t *testing.T) {
		input := basePlanInput()
		input.Policy.TTFTPolicy.Enabled = true
		input.TTFT = TTFTSnapshot{Routes: map[string]TTFTState{
			"route-cheap": {Samples: 100, P95MS: 20_000, FreshUntilMS: 200_000},
			"route-plus":  {Samples: 100, P95MS: 10_000, FreshUntilMS: 200_000},
		}}
		result, err := Plan(input)
		require.NoError(t, err)
		assert.Equal(t, "route-plus", result.RouteID)
	})
}

func TestNoQueueDefaultSpillsAndShortWaitCanQueue(t *testing.T) {
	input := basePlanInput()
	input.Capacity = CapacitySnapshot{Domains: map[string]CapacityState{
		"cap-cheap": {Known: true, MaxInflight: 1, Inflight: 1, EstimatedWaitMS: 400},
	}}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, PlanAttempt, result.Kind)
	assert.Equal(t, "route-plus", result.RouteID)

	input.Policy.QueuePolicy = QueuePolicy{Mode: QueueModeShortWait, MaxWaitMS: 800, MinSavingPercent: 30}
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, PlanQueue, result.Kind)
	assert.Equal(t, "route-cheap", result.RouteID)
	assert.Equal(t, int64(400), result.MaxQueueWaitMS)

	input.Attempt.Requeued = true
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, PlanAttempt, result.Kind)
	assert.Equal(t, "route-plus", result.RouteID)
}

func TestStrongAffinityCapacityOverflowPrefersSameCacheDomain(t *testing.T) {
	routes := []CertifiedRoute{
		testRoute("route-cheap", "cheap", 101, "cache-cheap", "cap-cheap", false),
		testRoute("route-plus", "plus", 102, "cache-plus", "cap-plus", true),
		testRoute("route-plus-peer", "plus", 104, "cache-plus", "cap-plus-peer", true),
	}
	input := basePlanInput()
	input.Policy.AffinityMaxPremiumPercent = 200
	input.Catalog = testCatalog(routes...)
	input.Prices.RatiosPPM["route-plus-peer"] = 50_000
	input.Quality.Routes["route-plus-peer"] = QualityState{Phase: QualityHealthy}
	input.Capacity = CapacitySnapshot{Domains: map[string]CapacityState{
		"cap-plus": {Known: true, MaxInflight: 1, Inflight: 1},
	}}
	input.Affinity = &AffinityState{
		Strength:    AffinityStrong,
		ContractID:  testContractID,
		RouteID:     "route-plus",
		ChannelID:   102,
		CacheDomain: "cache-plus",
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus-peer", result.RouteID)
	assert.Equal(t, AffinityPreserve, result.AffinityDisposition)
	assert.True(t, result.CapacityOverflow)
}

func TestContractOverrideSelectsPoolAndManualExactRoute(t *testing.T) {
	input := basePlanInput()
	input.Policy.Version = LegacyPolicyVersion
	contract := input.Catalog.Contracts[testContractID]
	contract.Pools["stable-v1"] = CertifiedPool{Candidates: []CertifiedRoute{
		testRoute("route-pro", "pro", 103, "cache-pro", "cap-pro", true),
		contract.Pools[testPoolID].Candidates[1],
	}}
	input.Catalog.Contracts[testContractID] = contract
	input.Prices.RatiosPPM["route-pro"] = 100_000
	input.Quality.Routes["route-pro"] = QualityState{Phase: QualityHealthy}
	input.AllowedGroups = append(input.AllowedGroups, "pro")
	input.Policy.MaxEffectiveRatioPPM = 100_000
	input.Policy.ContractOverrides[testContractID] = ContractOverride{
		PoolPolicy:       "stable-v1",
		OrderMode:        OrderManual,
		ManualRouteOrder: []string{"route-plus", "route-pro"},
	}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "stable-v1", result.PoolID)
	assert.Equal(t, "route-plus", result.RouteID)
}

func TestLegacyManualPolicyRejectsUnlistedFutureCertifiedRoute(t *testing.T) {
	input := basePlanInput()
	input.Policy.Version = LegacyPolicyVersion
	input.Policy.AllowFutureGroups = false
	input.Policy.ContractOverrides[testContractID] = ContractOverride{
		OrderMode:        OrderManual,
		ManualRouteOrder: []string{"route-plus"},
	}
	input.Policy.AllowFutureCertifiedRoutes = false

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectUnlistedManualRoute})
}

func TestManualGroupOrderUsesUserVisibleGroupsWithoutRouteIDs(t *testing.T) {
	input := basePlanInput()
	input.Policy.OrderMode = OrderManual
	input.Policy.ManualGroupOrder = []string{"plus", "cheap"}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)

	input.Policy.AllowFutureGroups = false
	input.Policy.ManualGroupOrder = []string{"plus"}
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
	assert.Contains(t, result.Rejections, Rejection{RouteID: "route-cheap", Reason: RejectGroupNotSelected})
}

func TestV4FixedGroupScopeAppliesToEveryAutomaticStrategy(t *testing.T) {
	for _, strategy := range []Strategy{
		StrategyPrice,
		StrategyStability,
		StrategyLatency,
		StrategyBalanced,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			input := basePlanInput()
			input.Policy.Strategy = strategy
			input.Policy.AllowFutureGroups = false
			input.Policy.ManualGroupOrder = []string{"plus"}

			result, err := Plan(input)
			require.NoError(t, err)
			assert.Equal(t, "route-plus", result.RouteID)
			assert.Contains(t, result.Rejections, Rejection{
				RouteID: "route-cheap", Reason: RejectGroupNotSelected,
			})
		})
	}
}

func TestPlanErrorIdentityIsPreserved(t *testing.T) {
	input := basePlanInput()
	input.Policy.Enabled = false
	_, err := Plan(input)
	assert.True(t, errors.Is(err, ErrPolicyDisabled))
}
