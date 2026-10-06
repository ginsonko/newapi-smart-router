package smartrouter

import (
	"fmt"
	"math"
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

func TestEstimateActualInputCostUsesEachCacheBucketAndColdStartPrior(t *testing.T) {
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
	assert.Equal(t, int64(19_000), static.EffectivePPM)
	assert.Equal(t, ActualInputCostOptimistic, static.Source)

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

func TestEstimateActualInputCostLegacyEvidenceExpiresAfterOneHour(t *testing.T) {
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

	insideWindow := EstimateActualInputCostAt(
		price, price.ScorePPM, key, "", snapshot,
		eventMS+actualInputCostWindowHorizonMS-1,
	)
	assert.Equal(t, ActualInputCostObserved, insideWindow.Source)
	assert.Equal(t, int64(100_000), insideWindow.EffectivePPM)
	assert.Zero(t, insideWindow.CacheReadRatePPM)

	expired := EstimateActualInputCostAt(
		price, price.ScorePPM, key, "", snapshot,
		eventMS+actualInputCostWindowHorizonMS+1,
	)
	assert.Equal(t, ActualInputCostOptimistic, expired.Source)
	assert.Equal(t, int64(19_000), expired.EffectivePPM)
	assert.Equal(t, int64(900_000), expired.CacheReadRatePPM)
	assert.False(t, expired.HasRealEvidence)

	_, _, _, ok := ActualInputCostEffectiveCacheReadRatePPMAt(
		profile, eventMS+actualInputCostWindowHorizonMS+1,
	)
	assert.False(t, ok, "evidence outside the one-hour window must not remain visible")
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

func TestActualInputCostPPMForTokenMixUsesFrozenPriceFormula(t *testing.T) {
	price := actualInputTestPrice()
	projected, ok := ActualInputCostPPMForTokenMix(price, 1_000_000, 100, 900, 0, 0)
	require.True(t, ok)
	assert.Equal(t, int64(190_000), projected)

	_, ok = ActualInputCostPPMForTokenMix(price, 1_000_000, -1, 900, 0, 0)
	assert.False(t, ok)
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
	left := Candidate{Route: CertifiedRoute{RouteID: "left"}, RatioPPM: 20_000, Price: actualInputTestPrice(), ActualInputCostPPM: 200_000, ActualInputCostComparable: true, ActualInputCostSource: ActualInputCostObserved}
	rightPrice := actualInputTestPrice()
	rightPrice.ScorePPM = 120_000
	right := Candidate{Route: CertifiedRoute{RouteID: "right"}, RatioPPM: 30_000, Price: rightPrice, ActualInputCostPPM: 100_000, ActualInputCostComparable: true, ActualInputCostSource: ActualInputCostObserved}

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
		ActualInputCostPPM: 120_000, ActualInputCostComparable: true, ActualInputCostSource: ActualInputCostObserved, priceClassRank: 0,
	}
	right := Candidate{
		Route: CertifiedRoute{RouteID: "actually-cheaper"}, Price: rightPrice,
		ActualInputCostPPM: 80_000, ActualInputCostComparable: true, ActualInputCostSource: ActualInputCostObserved, priceClassRank: 1,
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
	assert.Equal(t, []string{"fresh", "old", "optimistic"}, []string{
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

func TestActualInputCostWindowSingleRealSampleBlendsWithDefaultReference(t *testing.T) {
	price := actualInputTestPrice()
	key := ActualInputCostKey("namespace", "upstream", "model", "endpoint", "openai")
	profile := ActualInputCostProfile{}
	profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
		Key: key, TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true, EventMS: 1_000,
	}, 1_000)
	estimate := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000)
	assert.Equal(t, ActualInputCostObserved, estimate.Source)
	assert.Equal(t, int64(1_000_000), *estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(901_000), estimate.CacheReadRatePPM,
		"one real sample contributes exactly 1% while the cold-start reference fills the remaining 99%")
	assert.Equal(t, int64(10_000), estimate.ConfidencePPM)
	assert.Equal(t, int64(1), estimate.Samples)

	stale := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000+actualInputCostWindowHorizonMS+1)
	assert.Equal(t, ActualInputCostOptimistic, stale.Source, "a route with only expired evidence still uses the bounded optimistic prior")
	assert.Equal(t, int64(900_000), stale.CacheReadRatePPM)
}

func TestActualInputCostRecentHistoryMixMatrix(t *testing.T) {
	price := actualInputTestPrice()
	for _, sampleCount := range []int{0, 1, 9, 10, 20, 99, 100} {
		t.Run(fmt.Sprintf("samples_%d", sampleCount), func(t *testing.T) {
			const nowMS = int64(10_000)
			profile := ActualInputCostProfile{History: ActualInputCostHistorySummary{
				Version: actualInputCostHistoryVersion, RegularShare: 0.8, CacheReadShare: 0.2,
				EffectiveSamples: 200, TotalSamples: 200, LastEventMS: nowMS - 1,
			}}
			for index := 0; index < sampleCount; index++ {
				profile.Window = append(profile.Window, ActualInputCostWindowSample{
					ObservationHash: fmt.Sprintf("mix-%d", index), EventMS: nowMS,
					TotalInputTokens: 100, CacheReadTokens: 100,
				})
			}
			estimate := EstimateActualInputCostAt(
				price, price.ScorePPM, "route", "", ActualInputCostSnapshot{"route": profile}, nowMS,
			)
			wantRate := int64(math.Round((float64(sampleCount)/100 + (1-float64(sampleCount)/100)*0.2) * 1_000_000))
			assert.Equal(t, wantRate, estimate.CacheReadRatePPM)
			assert.Equal(t, int64(sampleCount)*10_000, estimate.ConfidencePPM)
			assert.Equal(t, int64(200), estimate.HistoricalSamples)
			if sampleCount == 0 {
				assert.Equal(t, ActualInputCostHistorical, estimate.Source)
				assert.Nil(t, estimate.ObservedCacheReadRatePPM)
			} else {
				assert.Equal(t, ActualInputCostObserved, estimate.Source)
				require.NotNil(t, estimate.ObservedCacheReadRatePPM)
				assert.Equal(t, int64(1_000_000), *estimate.ObservedCacheReadRatePPM)
			}
		})
	}
}

func TestActualInputCostRecentWindowUsesExactThirtyMinuteHalfLife(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		age       time.Duration
		wantShare float64
	}{
		{name: "thirty_minutes", age: 30 * time.Minute, wantShare: 1.0 / 1.5},
		{name: "sixty_minutes", age: 60 * time.Minute, wantShare: 1.0 / 1.25},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			nowMS := int64(time.Hour/time.Millisecond) + 1_000
			profile := ActualInputCostProfile{}
			profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
				ObservationID: "old", Key: "route", EventMS: nowMS - testCase.age.Milliseconds(),
				TotalInputTokens: 100, RegularInputTokens: 100, Reliable: true,
			}, nowMS)
			profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
				ObservationID: "new", Key: "route", EventMS: nowMS,
				TotalInputTokens: 100, CacheReadTokens: 100, Reliable: true,
			}, nowMS)
			regular, read, _, _, _, _, count, ok := windowMixAt(profile, nowMS, false)
			require.True(t, ok)
			assert.Equal(t, 2, count)
			assert.InDelta(t, testCase.wantShare, read/(regular+read), 1e-12)
		})
	}
}

func TestActualInputCostHistoryCommitsOnlyAfterBoundedBatch(t *testing.T) {
	profile := ActualInputCostProfile{}
	for index := 0; index < actualInputCostHistoryBatchSize; index++ {
		nowMS := int64(1_000 + index)
		observation := ActualInputCostObservation{
			ObservationID: fmt.Sprintf("history-%d", index), Key: "route", EventMS: nowMS,
			TotalInputTokens: 100, RegularInputTokens: 50, CacheReadTokens: 50, Reliable: true,
		}
		profile = ApplyActualInputCostObservation(profile, observation, nowMS)
		profile = ApplyActualInputCostWindowObservation(profile, observation, nowMS)
		if index < actualInputCostHistoryBatchSize-1 {
			assert.Zero(t, profile.History.TotalSamples)
		}
	}
	assert.Equal(t, int64(actualInputCostHistoryBatchSize), profile.History.TotalSamples)
	assert.Empty(t, profile.History.Pending)
	assert.InDelta(t, 0.5, profile.History.CacheReadShare, 1e-12)
	assert.InDelta(t, 0.5, profile.History.RegularShare, 1e-12)
}

func TestActualInputCostApplyIsIdempotentAcrossRestartReplay(t *testing.T) {
	observation := ActualInputCostObservation{
		ObservationID: "request-one", Key: "route", PoolKey: "pool", EventMS: 1_000,
		RegularInputTokens: 10, CacheReadTokens: 90, TotalInputTokens: 100, Reliable: true,
	}
	profile := ApplyActualInputCostObservation(ActualInputCostProfile{}, observation, 1_000)
	profile = ApplyActualInputCostWindowObservation(profile, observation, 1_000)
	replayed := ApplyActualInputCostObservation(profile, observation, 2_000)
	replayed = ApplyActualInputCostWindowObservation(replayed, observation, 2_000)

	assert.Equal(t, profile.TotalSamples, replayed.TotalSamples)
	assert.Equal(t, profile.WeightedSamples, replayed.WeightedSamples)
	assert.Len(t, replayed.Window, 1)
}

func TestActualInputCostWindowTrimsTenPercentAtEverySufficientSampleCount(t *testing.T) {
	for _, sampleCount := range []int{9, 10, 37, 100} {
		t.Run(fmt.Sprintf("samples_%d", sampleCount), func(t *testing.T) {
			profile := ActualInputCostProfile{}
			trimEachSide := sampleCount / 10
			for i := 0; i < sampleCount; i++ {
				read := int64(50)
				if i < trimEachSide {
					read = 100
				} else if i >= sampleCount-trimEachSide {
					read = 0
				}
				profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
					Key: "route", TotalInputTokens: 100, RegularInputTokens: 100 - read, CacheReadTokens: read,
					Reliable: true, EventMS: int64(1_000 + i),
				}, int64(1_000+i))
			}

			regular, read, _, _, _, _, count, ok := windowMixAt(profile, 1_200, false)
			require.True(t, ok)
			assert.Equal(t, sampleCount, count, "confidence must report raw real observations before trimming")
			assert.InDelta(t, 0.5, read/(regular+read), 0.01)
		})
	}
}

func TestActualInputCostWindowUsesPerRequestRecencyWeights(t *testing.T) {
	profile := ActualInputCostProfile{}
	profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
		Key: "route", TotalInputTokens: 1_000_000, RegularInputTokens: 1_000_000,
		Reliable: true, EventMS: 1_000,
	}, 1_000)
	profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
		Key: "route", TotalInputTokens: 100, CacheReadTokens: 100,
		Reliable: true, EventMS: 1_000 + int64(10*time.Minute/time.Millisecond),
	}, 1_000+int64(10*time.Minute/time.Millisecond))

	regular, read, _, _, _, _, count, ok := windowMixAt(
		profile, 1_000+int64(10*time.Minute/time.Millisecond), false,
	)
	require.True(t, ok)
	assert.Equal(t, 2, count)
	assert.InDelta(t, 1.0/(1.0+math.Pow(2, -1.0/3.0)), read/(regular+read), 0.001,
		"a million-token old request must not overwhelm a newer request solely because it is larger")
}

func TestTieredExpressionActualInputCostUsesObservedMixWithoutCachePriceGate(t *testing.T) {
	price := RoutePrice{
		BillingMode: "tiered_expr", BillingUnit: "per_token",
		ComparisonClass: "tiered-static", ActualInputComparisonClass: "normalized-input-v2",
		ScorePPM: 100_000, StaticComparable: true,
		TieredExpression:       "tier(\"input\", p * 2 + cr * 0.2 + cc * 2.5 + cc1h * 4)",
		TieredInputPredictable: true,
	}
	key := ActualInputCostKey("channel:46:generation-7", "provider-sol", "gpt-5.6-sol", "openai-responses-v1", "openai")
	profile := ApplyActualInputCostWindowObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		Key: key, TotalInputTokens: 100, RegularInputTokens: 50, CacheReadTokens: 50,
		Reliable: true, EventMS: 1_000,
	}, 1_000)

	estimate := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000)
	require.Equal(t, ActualInputCostObserved, estimate.Source)
	require.NotNil(t, estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(500_000), *estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(896_000), estimate.CacheReadRatePPM)
	assert.Equal(t, int64(19_360), estimate.EffectivePPM)
	assert.True(t, estimate.Comparable)
	assert.Equal(t, int64(1), estimate.Samples)

	defaultReference := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{}, 1_000)
	assert.Equal(t, ActualInputCostOptimistic, defaultReference.Source)
	assert.Equal(t, int64(900_000), defaultReference.CacheReadRatePPM)
	assert.Equal(t, int64(19_000), defaultReference.EffectivePPM)
}

func TestTieredExpressionHistoricalCostUsesExactExpressionHash(t *testing.T) {
	price := RoutePrice{
		BillingMode: "tiered_expr", BillingUnit: "per_token",
		ComparisonClass: "tiered-static", ActualInputComparisonClass: "normalized-input-v2",
		ScorePPM: 100_000, StaticComparable: true,
		TieredExpression:     `tier("input", p * 2 + cr * 0.2)`,
		TieredExpressionHash: "revision-a", TieredInputPredictable: true,
	}
	key := ActualInputCostKey("namespace", "provider-sol", "gpt-5.6-sol", "openai-responses-v1", "openai")
	profile := ApplyActualInputCostWindowObservation(ActualInputCostProfile{}, ActualInputCostObservation{
		ObservationID: "request-a", Key: key, TotalInputTokens: 100,
		RegularInputTokens: 20, CacheReadTokens: 80, Reliable: true, EventMS: 1_000,
		TieredExpressionHash: "revision-a", TieredInputPriceMicros: 2_500_000,
	}, 1_000)

	estimate := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000)
	require.Equal(t, ActualInputCostObserved, estimate.Source)
	assert.Equal(t, ActualInputCostCostSourceRatio, estimate.CostSource)
	assert.Equal(t, int64(1), estimate.CostSamples)
	require.NotNil(t, estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(800_000), *estimate.ObservedCacheReadRatePPM)
	assert.Equal(t, int64(899_000), estimate.CacheReadRatePPM)
	assert.Equal(t, int64(19_090), estimate.EffectivePPM)
	assert.True(t, estimate.Comparable)

	price.TieredExpressionHash = "revision-b"
	revised := EstimateActualInputCostAt(price, price.ScorePPM, key, "", ActualInputCostSnapshot{key: profile}, 1_000)
	assert.True(t, revised.HasRealEvidence, "price revisions must not hide cache facts")
	assert.Equal(t, int64(899_000), revised.CacheReadRatePPM)
	assert.Equal(t, int64(1), revised.CostSamples)
	assert.Equal(t, int64(19_090), revised.EffectivePPM,
		"a new expression revision uses its static cheapest tier; old cost samples never supply the price")
	assert.True(t, revised.Comparable)
}

func TestTieredExpressionHistoricalCostTrimsEachTailAndWeightsRecentSamples(t *testing.T) {
	price := RoutePrice{
		BillingMode: "tiered_expr", BillingUnit: "per_token",
		ComparisonClass: "tiered-static", ActualInputComparisonClass: "normalized-input-v2",
		ScorePPM: 100_000, StaticComparable: true,
		TieredExpression: `tier("input", p * 2 + cr * 0.2)`, TieredExpressionHash: "revision-a", TieredInputPredictable: true,
	}
	const nowMS = int64(10_000)
	profile := ActualInputCostProfile{}
	for index := 0; index < 10; index++ {
		cost := int64(2_000_000)
		if index == 0 {
			cost = 0
		}
		if index == 9 {
			cost = 20_000_000
		}
		profile = ApplyActualInputCostWindowObservation(profile, ActualInputCostObservation{
			ObservationID: fmt.Sprintf("request-%d", index), Key: "route", TotalInputTokens: 100,
			RegularInputTokens: 50, CacheReadTokens: 50, Reliable: true, EventMS: nowMS - int64(index),
			TieredExpressionHash: "revision-a", TieredInputPriceMicros: cost,
		}, nowMS)
	}
	estimate := EstimateActualInputCostAt(price, price.ScorePPM, "route", "", ActualInputCostSnapshot{"route": profile}, nowMS)
	assert.Equal(t, int64(10), estimate.CostSamples)
	assert.Equal(t, int64(22_600), estimate.EffectivePPM)
	assert.Equal(t, ActualInputCostCostSourceRatio, estimate.CostSource)
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
