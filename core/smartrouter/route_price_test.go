package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPriceSnapshotKeepsLegacyCompatibilityAndValidatesDescriptors(t *testing.T) {
	legacy := PriceSnapshot{Version: "legacy", RatiosPPM: map[string]int64{"route": 20_000}}
	require.NoError(t, legacy.Validate())
	assert.Equal(t, int64(20_000), routePriceForCandidate(legacy, "route", 20_000).ScorePPM)

	modern := PriceSnapshot{
		Version:   "modern",
		RatiosPPM: map[string]int64{"route": 20_000},
		RoutePrices: map[string]RoutePrice{"route": {
			BillingMode: "fixed_price", BillingUnit: "per_second",
			ComparisonClass: "fixed:per_second", ScorePPM: 125_000,
			StaticComparable: true, Scope: "group_model", Revision: "sha256:test",
		}},
	}
	require.NoError(t, modern.Validate())
	assert.Equal(t, int64(125_000), routePriceForCandidate(modern, "route", 20_000).ScorePPM)

	broken := modern
	broken.RoutePrices = map[string]RoutePrice{"route": {BillingMode: "fixed_price", BillingUnit: "per_second", StaticComparable: true}}
	require.Error(t, broken.Validate())
}

func TestIncompatibleRoutePricesAreNeverComparable(t *testing.T) {
	perSecond := RoutePrice{
		BillingMode: "fixed_price", BillingUnit: "per_second",
		ComparisonClass: "fixed:per_second", ScorePPM: 100_000, StaticComparable: true,
	}
	perRequest := RoutePrice{
		BillingMode: "fixed_price", BillingUnit: "per_request",
		ComparisonClass: "fixed:per_request", ScorePPM: 1, StaticComparable: true,
	}
	assert.False(t, perSecond.ComparableWith(perRequest))
}
