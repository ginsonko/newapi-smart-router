package smartrouter

import "math"

const mediaRequestComparisonClass = "media_request_cost_v1"

// QuoteMedia converts heterogeneous media billing units into one request cost.
// The returned descriptor is used only by the planner.
func (price RoutePrice) QuoteMedia(shape *MediaRequestShape) (RoutePrice, RejectReason) {
	if shape == nil {
		return price, ""
	}
	if !price.StaticComparable || price.ScorePPM < 0 {
		return mediaIncomparablePrice(price, price.IncomparableReason), ""
	}
	if price.BillingMode == "tiered_expr" {
		return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
	}

	unit := price.BillingUnit
	// Legacy ratio-priced video tasks are billed by ModelPriceHelperPerCall even
	// when no explicit task profile exists.
	if shape.Kind == MediaKindVideo && price.BillingMode == "ratio" && unit == "per_token" {
		unit = "per_request"
	}

	multiplier := int64(1)
	divisor := int64(1)
	count := int64(1)
	if shape.OutputCountKnown {
		if shape.OutputCount <= 0 {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		count = shape.OutputCount
	}
	switch unit {
	case "per_request":
		multiplier = count
	case "per_second":
		if !shape.DurationKnown || shape.DurationSeconds <= 0 {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		if !safePositiveProduct(&multiplier, shape.DurationSeconds) || !safePositiveProduct(&multiplier, count) {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
	case "fixed_duration":
		if !shape.DurationKnown || shape.DurationSeconds <= 0 || price.FixedDurationSeconds <= 0 {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		if shape.DurationSeconds != int64(price.FixedDurationSeconds) {
			return price, RejectMediaDurationMismatch
		}
		multiplier = count
	case "per_token":
		if !shape.EstimatedTokensKnown || shape.EstimatedTokens <= 0 || shape.QuotaPerUnit <= 0 {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		multiplier = shape.EstimatedTokens
		divisor = shape.QuotaPerUnit
	default:
		return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
	}

	if price.BillingMode == "ratio" && unit != "per_token" {
		if divisor > math.MaxInt64/2 {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		divisor *= 2
	}
	if shape.Kind == MediaKindImage && price.BillingMode == "fixed_price" && shape.RequestPriceMultiplier > 0 {
		if !safePositiveProduct(&multiplier, shape.RequestPriceMultiplier) {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		if divisor > math.MaxInt64/PriceScoreScale {
			return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
		}
		divisor *= PriceScoreScale
	}

	score, ok := roundedPositiveScale(price.ScorePPM, multiplier, divisor)
	if !ok {
		return mediaIncomparablePrice(price, string(RejectMediaPriceUnavailable)), ""
	}
	quoted := price
	quoted.BillingUnit = unit
	quoted.ComparisonClass = mediaRequestComparisonClass
	quoted.ActualInputComparisonClass = ""
	quoted.ScorePPM = score
	quoted.StaticComparable = true
	quoted.CachePricingKnown = false
	quoted.CacheReadRatioPPM = 0
	quoted.CacheCreation5mRatioPPM = 0
	quoted.CacheCreation1hRatioPPM = 0
	quoted.IncomparableReason = ""
	return quoted, ""
}

func mediaIncomparablePrice(price RoutePrice, reason string) RoutePrice {
	if reason == "" {
		reason = string(RejectMediaPriceUnavailable)
	}
	quoted := price
	quoted.ComparisonClass = ""
	quoted.ActualInputComparisonClass = ""
	quoted.ScorePPM = 0
	quoted.StaticComparable = false
	quoted.CachePricingKnown = false
	quoted.CacheReadRatioPPM = 0
	quoted.CacheCreation5mRatioPPM = 0
	quoted.CacheCreation1hRatioPPM = 0
	quoted.IncomparableReason = reason
	return quoted
}

func safePositiveProduct(value *int64, factor int64) bool {
	if value == nil || *value < 0 || factor < 0 {
		return false
	}
	if *value != 0 && factor > math.MaxInt64 / *value {
		return false
	}
	*value *= factor
	return true
}

func roundedPositiveScale(value, multiplier, divisor int64) (int64, bool) {
	if value < 0 || multiplier < 0 || divisor <= 0 {
		return 0, false
	}
	if value != 0 && multiplier > math.MaxInt64/value {
		return 0, false
	}
	product := value * multiplier
	if product > math.MaxInt64-divisor/2 {
		return 0, false
	}
	return (product + divisor/2) / divisor, true
}
