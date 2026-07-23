package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cacheEconomyCandidate(routeID string, ratio int64, namespace string) Candidate {
	return Candidate{Route: CertifiedRoute{
		RouteID: routeID, ChannelID: 1, CacheDomain: "cache", CacheNamespaceRevision: namespace,
	}, RatioPPM: ratio}
}

func readyCacheEconomySnapshot() *CacheEconomySnapshot {
	return &CacheEconomySnapshot{
		FeatureVersion: CacheEconomyFeatureVersion, ModelVersion: CacheEconomyChampionVersion,
		Samples: 30, MinimumSamples: 8, ConfidencePPM: 900_000, MinimumConfidencePPM: 700_000,
		PredictionErrorPPM: 20_000, WarmCostPerMillionContext: 400_000,
		ColdCostPerMillionContext: 1_400_000, ContextGrowthTokens: 10_000,
		CompressionThresholdTokens: 200_000, CacheSurvivalPPM: 0,
		JointSurvivalPPM: []int64{1_000_000, 850_000, 700_000, 550_000, 400_000},
	}
}

func TestEvaluateCacheEconomyKeepsWarmRouteWhenRebuildCostsMore(t *testing.T) {
	snapshot := readyCacheEconomySnapshot()
	current := cacheEconomyCandidate("warm", 100_000, "warm-v1")
	target := cacheEconomyCandidate("cheap", 90_000, "cheap-v1")

	decision := EvaluateCacheEconomy(snapshot, 150_000, current, target)

	assert.Equal(t, CacheEconomyKeep, decision.Action)
	assert.Contains(t, []CacheEconomyReason{CacheEconomyReasonKeepCostsLess, CacheEconomyReasonUncertain}, decision.Reason)
	assert.Greater(t, decision.SwitchCostScore, int64(0))
}

func TestEvaluateCacheEconomySwitchesWhenFiniteHorizonSavingsCoverRebuild(t *testing.T) {
	snapshot := readyCacheEconomySnapshot()
	snapshot.CompressionThresholdTokens = 500_000
	snapshot.ColdCostPerMillionContext = 600_000
	current := cacheEconomyCandidate("warm", 160_000, "warm-v1")
	target := cacheEconomyCandidate("cheap", 50_000, "cheap-v1")

	decision := EvaluateCacheEconomy(snapshot, 80_000, current, target)

	assert.Equal(t, CacheEconomySwitch, decision.Action)
	assert.Equal(t, CacheEconomyReasonSwitchCostsLess, decision.Reason)
	assert.Positive(t, decision.BenefitLowerBound)
	assert.Positive(t, decision.PredictedUnitCost)
	assert.Greater(t, decision.BenefitProbabilityPPM, CacheEconomyProbabilityScale/2)
}

func TestEvaluateCacheEconomySharedNamespaceImmediatelyUsesCheaperRoute(t *testing.T) {
	current := cacheEconomyCandidate("warm", 160_000, "shared-v1")
	target := cacheEconomyCandidate("cheap", 50_000, "shared-v1")

	decision := EvaluateCacheEconomy(nil, 80_000, current, target)
	assert.Equal(t, CacheEconomyFallback, decision.Action)

	decision = EvaluateCacheEconomy(&CacheEconomySnapshot{}, 80_000, current, target)
	assert.Equal(t, CacheEconomySwitch, decision.Action)
	assert.Equal(t, CacheEconomyReasonSharedCache, decision.Reason)
	assert.True(t, decision.CounterfactualKnown)
	assert.Equal(t, CacheEconomyProbabilityScale, decision.BenefitProbabilityPPM)
}

func TestEvaluateCacheEconomyInsufficientEvidenceFallsBack(t *testing.T) {
	snapshot := readyCacheEconomySnapshot()
	snapshot.Samples = 2
	decision := EvaluateCacheEconomy(
		snapshot, 80_000,
		cacheEconomyCandidate("warm", 100_000, "warm-v1"),
		cacheEconomyCandidate("cheap", 50_000, "cheap-v1"),
	)
	assert.Equal(t, CacheEconomyFallback, decision.Action)
	assert.Equal(t, CacheEconomyReasonLearning, decision.Reason)
}

func TestEvaluateCacheEconomyOnlyCreditsExactTargetNamespaceSurvival(t *testing.T) {
	snapshot := readyCacheEconomySnapshot()
	snapshot.CacheSurvivalPPM = CacheEconomyProbabilityScale
	snapshot.NamespaceSurvivalPPM = map[string]int64{
		"cache:another-namespace": CacheEconomyProbabilityScale,
	}
	current := cacheEconomyCandidate("current", 50_000, "current-v1")
	target := cacheEconomyCandidate("target", 20_000, "target-v1")

	unseen := EvaluateCacheEconomy(snapshot, 150_000, current, target)
	snapshot.NamespaceSurvivalPPM[target.Route.CacheNamespaceIdentity()] = CacheEconomyProbabilityScale
	seen := EvaluateCacheEconomy(snapshot, 150_000, current, target)

	assert.Greater(t, unseen.SwitchCostScore, seen.SwitchCostScore)
}

func TestPolicyCompressionThresholdsAreCanonicalAndUnique(t *testing.T) {
	policy := NormalizePolicy(Policy{
		Version: PolicyVersion, Enabled: true, Strategy: StrategyPrice,
		MaxEffectiveRatioPPM: 1, HealthGuard: true, AllowFutureGroups: true,
		CompressionThresholds: []ModelCompressionThreshold{
			{Model: " model-b ", Tokens: 20_000}, {Model: "model-a", Tokens: 10_000},
		},
	})
	require.NoError(t, policy.Validate())
	assert.Equal(t, "model-a", policy.CompressionThresholds[0].Model)
	threshold, ok := policy.CompressionThresholdForModel("model-b")
	assert.True(t, ok)
	assert.Equal(t, 20_000, threshold)

	policy.CompressionThresholds = append(policy.CompressionThresholds, ModelCompressionThreshold{Model: "model-a", Tokens: 30_000})
	assert.Error(t, policy.Validate())
}
