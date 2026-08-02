package smartrouter

import (
	"errors"
	"fmt"
)

const (
	PriceScoreScale             = int64(1_000_000)
	legacyPriceComparisonClass  = "legacy_group_ratio"
	legacyPriceBillingMode      = "legacy_ratio"
	legacyPriceBillingUnit      = "group_ratio"
	PriceIncomparableInvalid    = "invalid_price_descriptor"
	PriceIncomparableUnresolved = "model_price_unresolved"
)

// RoutePrice describes a static route price without changing the legacy group
// ratio ceiling. ScorePPM is meaningful only inside the same ComparisonClass;
// different classes must never be ordered as if they shared one price unit.
type RoutePrice struct {
	BillingMode          string `json:"billing_mode"`
	BillingUnit          string `json:"billing_unit"`
	FixedDurationSeconds int    `json:"fixed_duration_seconds,omitempty"`
	ComparisonClass      string `json:"comparison_class,omitempty"`
	ScorePPM             int64  `json:"score_ppm,omitempty"`
	StaticComparable     bool   `json:"static_comparable"`
	Synthetic            bool   `json:"synthetic,omitempty"`
	Scope                string `json:"scope,omitempty"`
	Revision             string `json:"revision,omitempty"`
	IncomparableReason   string `json:"incomparable_reason,omitempty"`
}

func LegacyRoutePrice(ratioPPM int64) RoutePrice {
	return RoutePrice{
		BillingMode:      legacyPriceBillingMode,
		BillingUnit:      legacyPriceBillingUnit,
		ComparisonClass:  legacyPriceComparisonClass,
		ScorePPM:         ratioPPM,
		StaticComparable: true,
		Scope:            "legacy",
	}
}

func (price RoutePrice) Validate() error {
	if price.BillingMode == "" || price.BillingUnit == "" {
		return errors.New("smartrouter: route price requires billing mode and unit")
	}
	if price.FixedDurationSeconds < 0 {
		return errors.New("smartrouter: route price fixed duration cannot be negative")
	}
	if price.StaticComparable {
		if price.ComparisonClass == "" {
			return errors.New("smartrouter: comparable route price requires comparison_class")
		}
		if price.ScorePPM < 0 {
			return errors.New("smartrouter: comparable route price score cannot be negative")
		}
		return nil
	}
	if price.IncomparableReason == "" {
		return errors.New("smartrouter: incomparable route price requires a reason")
	}
	return nil
}

func (price RoutePrice) ComparableWith(other RoutePrice) bool {
	return price.StaticComparable && other.StaticComparable &&
		price.ComparisonClass != "" && price.ComparisonClass == other.ComparisonClass
}

func invalidRoutePrice(reason string) RoutePrice {
	if reason == "" {
		reason = PriceIncomparableInvalid
	}
	return RoutePrice{
		BillingMode:        "unknown",
		BillingUnit:        "unknown",
		IncomparableReason: reason,
	}
}

func routePriceForCandidate(snapshot PriceSnapshot, routeID string, ratioPPM int64) RoutePrice {
	if snapshot.RoutePrices == nil {
		return LegacyRoutePrice(ratioPPM)
	}
	price, exists := snapshot.RoutePrices[routeID]
	if !exists {
		return LegacyRoutePrice(ratioPPM)
	}
	if err := price.Validate(); err != nil {
		return invalidRoutePrice(fmt.Sprintf("%s: %v", PriceIncomparableInvalid, err))
	}
	return price
}

func candidateRoutePrice(candidate Candidate) RoutePrice {
	if candidate.Price.BillingMode == "" && candidate.RatioPPM >= 0 {
		return LegacyRoutePrice(candidate.RatioPPM)
	}
	if err := candidate.Price.Validate(); err != nil {
		return invalidRoutePrice(fmt.Sprintf("%s: %v", PriceIncomparableInvalid, err))
	}
	return candidate.Price
}

func comparableCandidatePrice(left, right Candidate) (int, bool) {
	leftPrice := candidateRoutePrice(left)
	rightPrice := candidateRoutePrice(right)
	if !leftPrice.ComparableWith(rightPrice) {
		return 0, false
	}
	switch {
	case leftPrice.ScorePPM < rightPrice.ScorePPM:
		return -1, true
	case leftPrice.ScorePPM > rightPrice.ScorePPM:
		return 1, true
	default:
		return 0, true
	}
}

func comparableCandidatePriceScores(left, right Candidate) (int64, int64, bool) {
	leftPrice := candidateRoutePrice(left)
	rightPrice := candidateRoutePrice(right)
	if !leftPrice.ComparableWith(rightPrice) {
		return 0, 0, false
	}
	return leftPrice.ScorePPM, rightPrice.ScorePPM, true
}
