package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQuoteMediaNormalizesRequestCostWithoutChangingBillingMetadata(t *testing.T) {
	tests := []struct {
		name      string
		price     RoutePrice
		shape     MediaRequestShape
		wantScore int64
	}{
		{
			name:      "fixed image applies count and image multiplier once",
			price:     RoutePrice{BillingMode: "fixed_price", BillingUnit: "per_request", ComparisonClass: "fixed", ScorePPM: 300_000, StaticComparable: true},
			shape:     MediaRequestShape{Kind: MediaKindImage, OutputCount: 2, OutputCountKnown: true, RequestPriceMultiplier: 1_500_000},
			wantScore: 900_000,
		},
		{
			name:      "per second applies duration and count",
			price:     RoutePrice{BillingMode: "fixed_price", BillingUnit: "per_second", ComparisonClass: "second", ScorePPM: 40_500, StaticComparable: true},
			shape:     MediaRequestShape{Kind: MediaKindVideo, DurationSeconds: 10, DurationKnown: true, OutputCount: 2, OutputCountKnown: true},
			wantScore: 810_000,
		},
		{
			name:      "legacy ratio video keeps task half-ratio basis",
			price:     RoutePrice{BillingMode: "ratio", BillingUnit: "per_token", ComparisonClass: "ratio", ScorePPM: 100_000, StaticComparable: true},
			shape:     MediaRequestShape{Kind: MediaKindVideo, OutputCount: 1, OutputCountKnown: true},
			wantScore: 50_000,
		},
		{
			name:      "token image converts model ratio to request cost",
			price:     RoutePrice{BillingMode: "ratio", BillingUnit: "per_token", ComparisonClass: "token", ScorePPM: 200_000, StaticComparable: true},
			shape:     MediaRequestShape{Kind: MediaKindImage, OutputCount: 1, OutputCountKnown: true, EstimatedTokens: 1_000, EstimatedTokensKnown: true, QuotaPerUnit: 500_000},
			wantScore: 400,
		},
		{
			name:      "fixed duration counts each output once",
			price:     RoutePrice{BillingMode: "fixed_price", BillingUnit: "fixed_duration", FixedDurationSeconds: 15, ComparisonClass: "fixed-duration", ScorePPM: 200_000, StaticComparable: true},
			shape:     MediaRequestShape{Kind: MediaKindVideo, DurationSeconds: 15, DurationKnown: true, OutputCount: 2, OutputCountKnown: true},
			wantScore: 400_000,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quoted, reason := test.price.QuoteMedia(&test.shape)
			require.Empty(t, reason)
			assert.Equal(t, mediaRequestComparisonClass, quoted.ComparisonClass)
			assert.Equal(t, test.wantScore, quoted.ScorePPM)
			assert.True(t, quoted.StaticComparable)
			assert.Equal(t, test.price.BillingMode, quoted.BillingMode)
		})
	}
}

func TestQuoteMediaKeepsUnknownPriceEligibleButRejectsKnownContradiction(t *testing.T) {
	perSecond := RoutePrice{BillingMode: "fixed_price", BillingUnit: "per_second", ComparisonClass: "second", ScorePPM: 10_000, StaticComparable: true}
	quoted, reason := perSecond.QuoteMedia(&MediaRequestShape{Kind: MediaKindVideo, OutputCount: 1, OutputCountKnown: true})
	require.Empty(t, reason)
	assert.False(t, quoted.StaticComparable)
	assert.Equal(t, string(RejectMediaPriceUnavailable), quoted.IncomparableReason)

	fixed := RoutePrice{BillingMode: "fixed_price", BillingUnit: "fixed_duration", FixedDurationSeconds: 15, ComparisonClass: "fixed", ScorePPM: 10_000, StaticComparable: true}
	_, reason = fixed.QuoteMedia(&MediaRequestShape{Kind: MediaKindVideo, DurationSeconds: 10, DurationKnown: true, OutputCount: 1, OutputCountKnown: true})
	assert.Equal(t, RejectMediaDurationMismatch, reason)
}

func TestPriceStrategyPrefersKnownMediaQuoteAndFallsBackToUnknown(t *testing.T) {
	unknown := testRoute("unknown", "cheap", 101, "cache-unknown", "cap-unknown", true)
	known := testRoute("known", "plus", 102, "cache-known", "cap-known", true)
	input := basePlanInput()
	input.Catalog = testCatalog(unknown, known)
	input.Prices = PriceSnapshot{
		Version:   "media-prices",
		RatiosPPM: map[string]int64{"unknown": 10_000, "known": 20_000},
		RoutePrices: map[string]RoutePrice{
			"unknown": {BillingMode: "fixed_price", BillingUnit: "per_second", IncomparableReason: "duration_unknown"},
			"known":   {BillingMode: "fixed_price", BillingUnit: "per_request", ComparisonClass: "known", ScorePPM: 20_000, StaticComparable: true},
		},
	}
	input.Quality.Routes = map[string]QualityState{
		"unknown": {Phase: QualityHealthy, StableSinceMS: 1},
		"known":   {Phase: QualityHealthy, StableSinceMS: 1},
	}
	input.Request.ReplayClass = ReplaySafeImage
	input.Request.Media = &MediaRequestShape{Kind: MediaKindImage, OutputCount: 1, OutputCountKnown: true, RequestPriceMultiplier: PriceScoreScale}

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "known", result.RouteID)

	input.Catalog = testCatalog(unknown)
	input.Prices.RatiosPPM = map[string]int64{"unknown": 10_000}
	delete(input.Prices.RoutePrices, "known")
	delete(input.Quality.Routes, "known")
	result, err = Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "unknown", result.RouteID)
	assert.False(t, result.Price.StaticComparable)
}
