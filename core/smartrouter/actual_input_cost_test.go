package smartrouter

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func actualInputTestPrice() RoutePrice {
	return RoutePrice{
		BillingMode: "ratio", BillingUnit: "per_token",
		ComparisonClass: "full-price", ActualInputComparisonClass: "token-input",
		ScorePPM: 100_000, StaticComparable: true, CachePricingKnown: true,
		CacheReadRatioPPM: 100_000, CacheCreation5mRatioPPM: 125_000, CacheCreation1hRatioPPM: 200_000,
	}
}

func TestApplyActualInputCostObservationRejectsInconsistentTokenMix(t *testing.T) {
	previous := ActualInputCostProfile{}
	unchanged := ApplyActualInputCostObservation(previous, ActualInputCostObservation{
		Key: "route", TotalInputTokens: 10, RegularInputTokens: 9, CacheReadTokens: 2, Reliable: true,
	}, 1000)
	assert.Equal(t, previous, unchanged)

	accepted := ApplyActualInputCostObservation(previous, ActualInputCostObservation{
		Key: "route", TotalInputTokens: 10, RegularInputTokens: 8, CacheReadTokens: 2, Reliable: true,
	}, 1000)
	assert.Equal(t, int64(1), accepted.TotalSamples)
	assert.Equal(t, float64(10), accepted.WeightedTotalInputTokens)
}

func TestEstimateActualInputCostExactFirstIgnoresPooledProfile(t *testing.T) {
	price := actualInputTestPrice()
	exactKey := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	poolKey := ActualInputCostPoolKey("upstream", "model", "endpoint", "openai")
	var exact, pool ActualInputCostProfile
	exact = ApplyActualInputCostObservation(exact, ActualInputCostObservation{
		Key: exactKey, TotalInputTokens: 1000, CacheReadTokens: 1000, Reliable: true,
	}, 1000)
	pool = ApplyActualInputCostObservation(pool, ActualInputCostObservation{
		Key: poolKey, TotalInputTokens: 1000, RegularInputTokens: 1000, Reliable: true,
	}, 1000)
	estimate := EstimateActualInputCostAt(price, price.ScorePPM, exactKey, poolKey, ActualInputCostSnapshot{
		exactKey: exact, poolKey: pool,
	}, 1000)
	assert.Equal(t, int64(10_000), estimate.EffectivePPM)
	assert.Equal(t, int64(1_000_000), estimate.CacheReadRatePPM)
	require.NotNil(t, estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(1_000_000), *estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, ActualInputCostObserved, estimate.Source)
	assert.Equal(t, int64(1000), estimate.LastObservedAtMS)
	assert.True(t, estimate.HasRealEvidence)
	assert.True(t, estimate.Comparable)
}

func TestEstimateActualInputCostUsesEachCacheBucketAndStaticFallback(t *testing.T) {
	price := actualInputTestPrice()
	exactKey := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")

	assertEstimate := func(t *testing.T, observation ActualInputCostObservation, want int64) {
		t.Helper()
		profile := ApplyActualInputCostObservation(ActualInputCostProfile{}, observation, 1000)
		estimate := EstimateActualInputCostAt(price, price.ScorePPM, exactKey, "", ActualInputCostSnapshot{exactKey: profile}, 1000)
		assert.Equal(t, want, estimate.EffectivePPM)
		assert.Equal(t, ActualInputCostObserved, estimate.Source)
	}

	static := EstimateActualInputCostAt(price, price.ScorePPM, exactKey, "", nil, 1000)
	assert.Equal(t, price.ScorePPM, static.EffectivePPM)
	assert.Equal(t, ActualInputCostStaticFallback, static.Source)

	optimistic := EstimateActualInputCostAt(price, price.ScorePPM, exactKey, "", ActualInputCostSnapshot{}, 1000)
	assert.Equal(t, int64(19_000), optimistic.EffectivePPM)
	assert.Equal(t, int64(900_000), optimistic.CacheReadRatePPM)
	assert.Equal(t, ActualInputCostOptimistic, optimistic.Source)
	assert.False(t, optimistic.HasRealEvidence)
	assert.Nil(t, optimistic.ObservedCacheReadRatePPM, "the hidden prior must not masquerade as measured data")

	assertEstimate(t, ActualInputCostObservation{Key: exactKey, TotalInputTokens: 100, RegularInputTokens: 100, Reliable: true}, 100_000)
	assertEstimate(t, ActualInputCostObservation{Key: exactKey, TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true}, 10_000)
	assertEstimate(t, ActualInputCostObservation{Key: exactKey, TotalInputTokens: 100, CacheCreation5mTokens: 100, Reliable: true}, 12_500)
	assertEstimate(t, ActualInputCostObservation{Key: exactKey, TotalInputTokens: 100, CacheCreation1hTokens: 100, Reliable: true}, 20_000)
}

func TestEstimateActualInputCostEvidenceAgesContinuouslyTowardHiddenPrior(t *testing.T) {
	price := actualInputTestPrice()
	const eventMS = int64(1_000)
	key := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	profile := ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: key, TotalInputTokens: 100, RegularInputTokens: 100, Reliable: true,
	}, eventMS)
	snapshot := ActualInputCostSnapshot{key: profile}

	fresh := EstimateActualInputCostAt(price, price.ScorePPM, key, "", snapshot, eventMS)
	assert.Equal(t, int64(100_000), fresh.EffectivePPM)
	assert.Zero(t, fresh.CacheReadRatePPM)

	after72h := EstimateActualInputCostAt(price, price.ScorePPM, key, "", snapshot, eventMS+int64(72*time.Hour/time.Millisecond))
	assert.Equal(t, int64(59_500), after72h.EffectivePPM)
	assert.Equal(t, int64(450_000), after72h.CacheReadRatePPM)

	after7d := EstimateActualInputCostAt(price, price.ScorePPM, key, "", snapshot, eventMS+int64(7*24*time.Hour/time.Millisecond))
	assert.InDelta(t, 72.14, float64(after7d.CacheReadRatePPM)/10_000, 0.02)
	assert.Less(t, after7d.EffectivePPM, after72h.EffectivePPM)
	assert.Greater(t, after7d.EffectivePPM, int64(19_000))

	effective, observed, age, ok := ActualInputCostEffectiveCacheReadRatePPMAt(
		profile, eventMS+int64(72*time.Hour/time.Millisecond),
	)
	require.True(t, ok)
	assert.Equal(t, int64(450_000), effective)
	assert.Zero(t, observed)
	assert.Equal(t, int64(72*time.Hour/time.Millisecond), age)
}

func TestEstimateActualInputCostMissingEvidenceCooldownAndReliableReset(t *testing.T) {
	price := actualInputTestPrice()
	key := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	profile := ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: key, TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true,
	}, 1_000)
	profile = ApplyActualInputCostObservation(profile, ActualInputCostObservation{
		Key: key, MissingEvidence: true,
	}, 2_000)
	firstMissing := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 2_000)
	assert.Equal(t, ActualInputCostOptimistic, firstMissing.Source)
	assert.Equal(t, int64(19_000), firstMissing.EffectivePPM)
	assert.True(t, firstMissing.Optimistic)

	profile = ApplyActualInputCostObservation(profile, ActualInputCostObservation{
		Key: key, MissingEvidence: true,
	}, 3_000)
	blocked := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 3_000)
	assert.Equal(t, ActualInputCostUnobservable, blocked.Source)
	assert.Equal(t, price.ScorePPM, blocked.EffectivePPM)
	assert.False(t, blocked.Optimistic)

	retryAt := int64(3_000) + int64(24*time.Hour/time.Millisecond)
	retry := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, retryAt)
	assert.Equal(t, ActualInputCostOptimistic, retry.Source)
	assert.True(t, retry.Optimistic)

	profile = ApplyActualInputCostObservation(profile, ActualInputCostObservation{
		Key: key, TotalInputTokens: 100, RegularInputTokens: 100, Reliable: true,
	}, retryAt)
	assert.Zero(t, profile.MissingEvidenceStreak)
	assert.Zero(t, profile.LastMissingEvidenceMS)
	reset := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, retryAt)
	assert.Equal(t, ActualInputCostObserved, reset.Source)
	assert.True(t, reset.HasRealEvidence)
}

func TestEstimateActualInputCostMalformedOrFutureEvidenceFallsBackStatic(t *testing.T) {
	price := actualInputTestPrice()
	key := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	for name, profile := range map[string]ActualInputCostProfile{
		"missing sample count": {
			Version: ActualInputCostFeatureVersion, WeightedRegularTokens: 1, WeightedTotalInputTokens: 1, LastEventMS: 1_000,
		},
		"future timestamp": {
			Version: ActualInputCostFeatureVersion, WeightedRegularTokens: 1, WeightedTotalInputTokens: 1,
			WeightedSamples: 1, TotalSamples: 1, LastEventMS: 2_000,
		},
		"negative missing streak": {Version: ActualInputCostFeatureVersion, MissingEvidenceStreak: -1},
	} {
		t.Run(name, func(t *testing.T) {
			estimate := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000)
			assert.Equal(t, ActualInputCostStaticFallback, estimate.Source)
			assert.Equal(t, price.ScorePPM, estimate.EffectivePPM)
		})
	}
}

func TestActualInputCostProfileMergePreservesZeroAndWeightedCacheRate(t *testing.T) {
	regular := ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: "regular", TotalInputTokens: 300, RegularInputTokens: 300, Reliable: true,
	}, 1000)
	cached := ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: "cached", TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true,
	}, 2000)

	merged, ok := MergeActualInputCostProfiles(regular, ActualInputCostProfile{}, cached)
	require.True(t, ok)
	assert.Equal(t, int64(2), merged.TotalSamples)
	assert.Equal(t, int64(2000), merged.LastEventMS)
	rate, ok := ActualInputCostCacheReadRatePPM(merged)
	require.True(t, ok)
	assert.Equal(t, int64(250_000), rate)

	zeroRate, ok := ActualInputCostCacheReadRatePPM(regular)
	require.True(t, ok)
	assert.Zero(t, zeroRate, "a reliable zero hit rate must remain visible")
	_, ok = ActualInputCostCacheReadRatePPM(ActualInputCostProfile{})
	assert.False(t, ok, "missing evidence must not be presented as zero percent")
}

func TestEstimateActualInputCostWeightsRecentSamplesWithoutAWindow(t *testing.T) {
	price := actualInputTestPrice()
	exactKey := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	profile := ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: exactKey, TotalInputTokens: 1000, RegularInputTokens: 1000, Reliable: true,
	}, 1000)
	profile = ApplyActualInputCostObservation(profile, ActualInputCostObservation{
		Key: exactKey, TotalInputTokens: 1000, CacheReadTokens: 1000, Reliable: true,
	}, 1000+int64(72*time.Hour/time.Millisecond))

	regular := profile.WeightedRegularTokens
	read := profile.WeightedCacheReadTokens
	assert.Greater(t, read, regular, "the recent sample must carry more weight after a half-life")
	estimate := EstimateActualInputCostAt(
		price, price.ScorePPM, exactKey, "", ActualInputCostSnapshot{exactKey: profile},
		1000+int64(72*time.Hour/time.Millisecond),
	)
	assert.Less(t, estimate.EffectivePPM, int64(50_000))
}

func TestActualInputCostPoolKeySeparatesUpstreamMappings(t *testing.T) {
	left := ActualInputCostPoolKey("provider-a-model", "canonical", "endpoint", "openai")
	right := ActualInputCostPoolKey("provider-b-model", "canonical", "endpoint", "openai")
	assert.NotEqual(t, left, right)
}

func TestActualInputCostRankingCanChangeOnlyOrdering(t *testing.T) {
	left := Candidate{Route: CertifiedRoute{RouteID: "left"}, RatioPPM: 20_000, Price: actualInputTestPrice(), ActualInputCostPPM: 200_000, ActualInputCostComparable: true}
	rightPrice := actualInputTestPrice()
	rightPrice.ScorePPM = 120_000
	right := Candidate{Route: CertifiedRoute{RouteID: "right"}, RatioPPM: 30_000, Price: rightPrice, ActualInputCostPPM: 100_000, ActualInputCostComparable: true}

	actual := []Candidate{left, right}
	sortCandidates(actual, StrategyPrice, false, false, true)
	assert.Equal(t, "right", actual[0].Route.RouteID)

	static := []Candidate{left, right}
	sortCandidates(static, StrategyPrice, false, false, false)
	assert.Equal(t, "left", static[0].Route.RouteID)
}

func TestActualInputCostRankingCrossesLegacyCachePriceClasses(t *testing.T) {
	leftPrice := actualInputTestPrice()
	leftPrice.ComparisonClass = "legacy-cache-class-a"
	rightPrice := actualInputTestPrice()
	rightPrice.ComparisonClass = "legacy-cache-class-b"

	left := Candidate{
		Route: CertifiedRoute{RouteID: "catalog-first"}, Price: leftPrice,
		ActualInputCostPPM: 120_000, ActualInputCostComparable: true, priceClassRank: 0,
	}
	right := Candidate{
		Route: CertifiedRoute{RouteID: "actually-cheaper"}, Price: rightPrice,
		ActualInputCostPPM: 80_000, ActualInputCostComparable: true, priceClassRank: 1,
	}

	actual := []Candidate{left, right}
	sortCandidates(actual, StrategyPrice, false, false, true)
	assert.Equal(t, "actually-cheaper", actual[0].Route.RouteID)

	static := []Candidate{left, right}
	sortCandidates(static, StrategyPrice, false, false, false)
	assert.Equal(t, "catalog-first", static[0].Route.RouteID)
}

func TestActualInputCostEqualCostPrefersUnobservedThenOldestEvidence(t *testing.T) {
	price := actualInputTestPrice()
	candidates := []Candidate{
		{
			Route: CertifiedRoute{RouteID: "fresh"}, Price: price,
			ActualInputCostPPM: 50_000, ActualInputCostComparable: true,
			ActualInputCostSource: ActualInputCostObserved, ActualInputCostObservedMS: 3_000,
		},
		{
			Route: CertifiedRoute{RouteID: "optimistic"}, Price: price,
			ActualInputCostPPM: 50_000, ActualInputCostComparable: true,
			ActualInputCostSource: ActualInputCostOptimistic,
		},
		{
			Route: CertifiedRoute{RouteID: "old"}, Price: price,
			ActualInputCostPPM: 50_000, ActualInputCostComparable: true,
			ActualInputCostSource: ActualInputCostObserved, ActualInputCostObservedMS: 1_000,
		},
	}

	sortCandidates(candidates, StrategyPrice, false, false, true)
	assert.Equal(t, []string{"optimistic", "old", "fresh"}, []string{
		candidates[0].Route.RouteID, candidates[1].Route.RouteID, candidates[2].Route.RouteID,
	})
}

func TestActualInputCostPriceRequiresInputClassWhenCachePricingIsKnown(t *testing.T) {
	price := actualInputTestPrice()
	price.ActualInputComparisonClass = ""
	require.Error(t, price.Validate())
}

func TestEstimateActualInputCostLookupRemainsBoundedWithMaximumSnapshot(t *testing.T) {
	price := actualInputTestPrice()
	const target = "target-route"
	snapshot := make(ActualInputCostSnapshot, 8192)
	for index := 0; index < 8191; index++ {
		snapshot[fmt.Sprintf("unrelated-%d", index)] = ActualInputCostProfile{
			Version: ActualInputCostFeatureVersion, WeightedRegularTokens: 1,
			WeightedTotalInputTokens: 1, WeightedSamples: 1, TotalSamples: 1, LastEventMS: 1_000,
		}
	}
	snapshot[target] = ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: target, TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true,
	}, 1_000)

	estimate := EstimateActualInputCostAt(price, price.ScorePPM, target, "", snapshot, 1_000)
	assert.Equal(t, int64(10_000), estimate.EffectivePPM)
	assert.Equal(t, int64(1_000_000), estimate.CacheReadRatePPM)
	allocations := testing.AllocsPerRun(1_000, func() {
		_ = EstimateActualInputCostAt(price, price.ScorePPM, target, "", snapshot, 1_000)
	})
	assert.LessOrEqual(t, allocations, 1.0, "ranking must remain an O(1) snapshot lookup without size-dependent allocation")
}

func BenchmarkEstimateActualInputCost8192Profiles(b *testing.B) {
	price := actualInputTestPrice()
	const target = "target-route"
	snapshot := make(ActualInputCostSnapshot, 8192)
	for index := 0; index < 8191; index++ {
		snapshot[fmt.Sprintf("unrelated-%d", index)] = ActualInputCostProfile{
			Version: ActualInputCostFeatureVersion, WeightedRegularTokens: 1,
			WeightedTotalInputTokens: 1, WeightedSamples: 1, TotalSamples: 1, LastEventMS: 1_000,
		}
	}
	snapshot[target] = ApplyActualInputCostObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: target, TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true,
	}, 1_000)

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		_ = EstimateActualInputCostAt(price, price.ScorePPM, target, "", snapshot, 1_000)
	}
}
