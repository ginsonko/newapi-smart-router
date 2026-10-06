package smartrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

)

// ActualInputCostFeatureVersion is deliberately independent from the
// session-level cache-economy model.  The two features answer different
// questions and must never share state.
const ActualInputCostFeatureVersion = "actual-input-cost-v1"

const (
	actualInputCostPPMScale                  = int64(1_000_000)
	actualInputCostEWMAWindow                = 100.0
	actualInputCostTimeHalfLifeMS            = float64(72 * time.Hour / time.Millisecond)
	actualInputCostEvidenceHalfLifeMS        = float64(72 * time.Hour / time.Millisecond)
	actualInputCostOptimisticCacheReadPPM    = int64(900_000)
	actualInputCostMissingEvidenceThreshold  = int64(2)
	actualInputCostMissingEvidenceCooldownMS = int64(24 * time.Hour / time.Millisecond)
	ActualInputCostEstimatorVersion          = "actual-input-cost-ranking-v3"
	actualInputCostWindowSize                = 100
	actualInputCostWindowHorizonMS           = int64(time.Hour / time.Millisecond)
	actualInputCostWindowHalfLifeMS          = float64(30 * time.Minute / time.Millisecond)
	actualInputCostHistorySampleHalfLife     = 500.0
	actualInputCostHistoryVersion            = "route-history-v1"
	actualInputCostHistoryBatchSize          = 20
	actualInputCostHistoryBatchIntervalMS    = int64(5 * time.Minute / time.Millisecond)
	actualInputCostHistoryAlphaMax           = 0.08
	actualInputCostHistoryDriftAlphaMax      = 0.12
)

// ActualInputCostWindowSample is the bounded, request-level evidence used by
// the current price-first ranking. It is intentionally token-mix only: no
// prompts, keys, or user identifiers are persisted.
type ActualInputCostWindowSample struct {
	// ObservationHash is an irreversible idempotency key. It lets independent
	// Web/Worker replicas union the same bounded window without persisting a
	// request id, user id, token id, or any request content.
	ObservationHash       string `json:"observation_hash,omitempty"`
	RegularInputTokens    int64  `json:"regular_input_tokens"`
	CacheReadTokens       int64  `json:"cache_read_tokens"`
	CacheCreation5mTokens int64  `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int64  `json:"cache_creation_1h_tokens"`
	TotalInputTokens      int64  `json:"total_input_tokens"`
	EventMS               int64  `json:"event_ms"`
	// TieredInputPriceMicros is the frozen-expression result for one million
	// normalized input tokens, before the group ratio. The expression hash is
	// required even when the value is zero, so price revisions cannot consume
	// stale evidence. No request parameter/header value is retained.
	TieredExpressionHash   string `json:"tiered_expression_hash,omitempty"`
	TieredInputPriceMicros int64  `json:"tiered_input_price_micros,omitempty"`
	// Warmup marks a cache namespace transition/creation observation. It is
	// consumed only by the established cache-rate estimator; it never changes
	// the unified cost formula itself.
	Warmup bool `json:"warmup,omitempty"`
}

// ActualInputCostProfile is a compact EWMA state.  It contains token counts,
// never prompts, user identifiers, keys, or response bodies.
type ActualInputCostProfile struct {
	Version                  string  `json:"version"`
	WeightedRegularTokens    float64 `json:"weighted_regular_tokens"`
	WeightedCacheReadTokens  float64 `json:"weighted_cache_read_tokens"`
	WeightedCacheCreation5m  float64 `json:"weighted_cache_creation_5m"`
	WeightedCacheCreation1h  float64 `json:"weighted_cache_creation_1h"`
	WeightedTotalInputTokens float64 `json:"weighted_total_input_tokens"`
	WeightedSamples          float64 `json:"weighted_samples"`
	TotalSamples             int64   `json:"total_samples"`
	LastEventMS              int64   `json:"last_event_ms"`
	MissingEvidenceStreak    int64   `json:"missing_evidence_streak,omitempty"`
	LastMissingEvidenceMS    int64   `json:"last_missing_evidence_at_ms,omitempty"`
	// Window is populated by the service learner for the R56 ranking model.
	// Keeping it optional preserves compatibility with R52/R55 Redis profiles.
	Window            []ActualInputCostWindowSample         `json:"window,omitempty"`
	History           ActualInputCostHistorySummary         `json:"history,omitempty"`
	TieredCostHistory map[string]ActualInputCostCostHistory `json:"tiered_cost_history,omitempty"`
}

// ActualInputCostHistorySummary is the constant-size, versioned route history
// that fills the part of the 100-sample ranking window not covered by recent
// traffic. Pending contains at most one five-minute/20-observation batch and
// never stores prompts, users, token ids, or raw request ids.
type ActualInputCostHistorySummary struct {
	Version                 string                        `json:"version,omitempty"`
	RegularShare            float64                       `json:"regular_share,omitempty"`
	CacheReadShare          float64                       `json:"cache_read_share,omitempty"`
	CacheCreation5mShare    float64                       `json:"cache_creation_5m_share,omitempty"`
	CacheCreation1hShare    float64                       `json:"cache_creation_1h_share,omitempty"`
	EffectiveSamples        float64                       `json:"effective_samples,omitempty"`
	TotalSamples            int64                         `json:"total_samples,omitempty"`
	LastEventMS             int64                         `json:"last_event_ms,omitempty"`
	MedianCacheReadRate     float64                       `json:"median_cache_read_rate,omitempty"`
	MADCacheReadRate        float64                       `json:"mad_cache_read_rate,omitempty"`
	DriftDirection          int                           `json:"drift_direction,omitempty"`
	ConsecutiveDriftBatches int                           `json:"consecutive_drift_batches,omitempty"`
	PendingStartedMS        int64                         `json:"pending_started_ms,omitempty"`
	Pending                 []ActualInputCostWindowSample `json:"pending,omitempty"`
}

type ActualInputCostCostHistory struct {
	ValueMicros      float64 `json:"value_micros"`
	EffectiveSamples float64 `json:"effective_samples"`
	TotalSamples     int64   `json:"total_samples"`
	LastEventMS      int64   `json:"last_event_ms"`
}

// ActualInputCostSnapshot is immutable after publication by the service
// learner.  Callers must treat the map and its values as read-only.
type ActualInputCostSnapshot map[string]ActualInputCostProfile

type ActualInputCostObservation struct {
	ObservationID          string
	Key                    string
	PoolKey                string
	EventMS                int64
	RegularInputTokens     int64
	CacheReadTokens        int64
	CacheCreation5mTokens  int64
	CacheCreation1hTokens  int64
	TotalInputTokens       int64
	Reliable               bool
	MissingEvidence        bool
	TieredExpressionHash   string
	TieredInputPriceMicros int64
	Warmup                 bool
}

type ActualInputCostEstimate struct {
	EffectivePPM               int64  `json:"effective_ppm"`
	CacheReadRatePPM           int64  `json:"cache_read_rate_ppm,omitempty"`
	ObservedCacheReadRatePPM   *int64 `json:"observed_cache_read_rate_ppm,omitempty"`
	Source                     string `json:"source"`
	Samples                    int64  `json:"samples,omitempty"`
	LastObservedAtMS           int64  `json:"last_observed_at_ms,omitempty"`
	EvidenceAgeMS              int64  `json:"evidence_age_ms,omitempty"`
	HasRealEvidence            bool   `json:"has_real_evidence"`
	Optimistic                 bool   `json:"optimistic"`
	Comparable                 bool   `json:"comparable"`
	CostSamples                int64  `json:"cost_samples,omitempty"`
	CostSource                 string `json:"cost_source,omitempty"`
	HistoricalCacheReadRatePPM *int64 `json:"historical_cache_read_rate_ppm,omitempty"`
	HistoricalSamples          int64  `json:"historical_samples,omitempty"`
	ConfidencePPM              int64  `json:"confidence_ppm,omitempty"`
	EstimatorVersion           string `json:"estimator_version,omitempty"`
}

const (
	ActualInputCostObserved               = "observed"
	ActualInputCostPooledPrior            = "pooled_prior"
	ActualInputCostOptimistic             = "optimistic_unobserved"
	ActualInputCostUnobservable           = "unobservable_static"
	ActualInputCostStaticFallback         = "static_fallback"
	ActualInputCostObservedDefaultContext = "observed_mix_default_context"
	ActualInputCostHistorical             = "route_history"

	ActualInputCostCostSourceRatio          = "ratio_contract"
	ActualInputCostCostSourceTieredObserved = "tiered_context_observed"
	ActualInputCostCostSourceTieredHistory  = "tiered_context_history"
	ActualInputCostCostSourceTieredDefault  = "tiered_default_context"
	ActualInputCostCostSourceUnavailable    = "unavailable"
)

// ActualInputCostKey identifies one physical cache namespace and model
// contract. Hashing keeps Redis keys bounded and avoids leaking model names in
// operational key listings.
func ActualInputCostKey(cacheNamespace, upstreamModel, canonicalModel, endpoint, usageSemantic string) string {
	return actualInputCostDigest("route", cacheNamespace, upstreamModel, canonicalModel, endpoint, usageSemantic)
}

// ActualInputCostPoolKey identifies the legacy cross-namespace aggregate kept
// in the persisted v1 snapshot for compatibility and visibility. R42 route
// ordering deliberately ignores it: the first exact physical-route sample
// takes full control and unrelated cache namespaces never dilute it.
func ActualInputCostPoolKey(upstreamModel, canonicalModel, endpoint, usageSemantic string) string {
	return actualInputCostDigest("pool", upstreamModel, canonicalModel, endpoint, usageSemantic)
}

func actualInputCostDigest(kind string, parts ...string) string {
	joined := kind
	for _, part := range parts {
		joined += "\x00" + strings.TrimSpace(part)
	}
	digest := sha256.Sum256([]byte(joined))
	return ActualInputCostFeatureVersion + ":" + hex.EncodeToString(digest[:])
}

func actualInputCostObservationDigest(kind string, parts ...string) string {
	joined := kind
	for _, part := range parts {
		joined += "\x00" + strings.TrimSpace(part)
	}
	digest := sha256.Sum256([]byte(joined))
	// 128 bits keeps replica idempotency collision risk negligible while
	// bounding the hot Redis JSON window substantially below a full route key.
	return hex.EncodeToString(digest[:16])
}

func (profile ActualInputCostProfile) valid() bool {
	if profile.Version != "" && profile.Version != ActualInputCostFeatureVersion {
		return false
	}
	if profile.TotalSamples < 0 || profile.LastEventMS < 0 ||
		profile.MissingEvidenceStreak < 0 || profile.LastMissingEvidenceMS < 0 {
		return false
	}
	return (profile.MissingEvidenceStreak == 0) == (profile.LastMissingEvidenceMS == 0)
}

// ApplyActualInputCostObservation applies sample-count and time decay.  A
// malformed or unreliable sample is ignored rather than becoming evidence.
func ApplyActualInputCostObservation(previous ActualInputCostProfile, observation ActualInputCostObservation, nowMS int64) ActualInputCostProfile {
	if observation.Key == "" {
		return previous
	}
	if nowMS <= 0 {
		nowMS = observation.EventMS
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	if !previous.valid() {
		previous = ActualInputCostProfile{}
	}
	if ActualInputCostProfileHasObservation(previous, observation.ObservationID) {
		return previous
	}
	if observation.MissingEvidence {
		if observation.Reliable {
			return previous
		}
		if previous.MissingEvidenceStreak < math.MaxInt64 {
			previous.MissingEvidenceStreak++
		}
		previous.LastMissingEvidenceMS = nowMS
		previous.Version = ActualInputCostFeatureVersion
		return previous
	}
	if !observation.Reliable || observation.TotalInputTokens <= 0 ||
		observation.RegularInputTokens < 0 || observation.CacheReadTokens < 0 ||
		observation.CacheCreation5mTokens < 0 || observation.CacheCreation1hTokens < 0 {
		return previous
	}
	remaining := observation.TotalInputTokens
	for _, component := range []int64{
		observation.RegularInputTokens,
		observation.CacheReadTokens,
		observation.CacheCreation5mTokens,
	} {
		if component > remaining {
			return previous
		}
		remaining -= component
	}
	if observation.CacheCreation1hTokens != remaining {
		return previous
	}
	eventMS := observation.EventMS
	if eventMS <= 0 || eventMS > nowMS {
		eventMS = nowMS
	}
	decay := 1.0
	if previous.LastEventMS > 0 {
		delta := float64(eventMS - previous.LastEventMS)
		if delta < 0 {
			delta = 0
		}
		sampleDecay := 1.0 - 2.0/(actualInputCostEWMAWindow+1.0)
		timeDecay := math.Pow(2, -delta/actualInputCostTimeHalfLifeMS)
		decay = sampleDecay * timeDecay
	}
	if decay < 0 || decay > 1 || math.IsNaN(decay) {
		decay = 0
	}
	previous.WeightedRegularTokens *= decay
	previous.WeightedCacheReadTokens *= decay
	previous.WeightedCacheCreation5m *= decay
	previous.WeightedCacheCreation1h *= decay
	previous.WeightedTotalInputTokens *= decay
	previous.WeightedSamples *= decay
	previous.WeightedRegularTokens += float64(observation.RegularInputTokens)
	previous.WeightedCacheReadTokens += float64(observation.CacheReadTokens)
	previous.WeightedCacheCreation5m += float64(observation.CacheCreation5mTokens)
	previous.WeightedCacheCreation1h += float64(observation.CacheCreation1hTokens)
	previous.WeightedTotalInputTokens += float64(observation.TotalInputTokens)
	previous.WeightedSamples += 1
	previous.TotalSamples++
	if eventMS > previous.LastEventMS {
		previous.LastEventMS = eventMS
	}
	previous.MissingEvidenceStreak = 0
	previous.LastMissingEvidenceMS = 0
	previous.Version = ActualInputCostFeatureVersion
	return previous
}

// ApplyActualInputCostWindowObservation records the newest bounded sample
// window. It deliberately lives beside the legacy EWMA updater so a rollback
// can still read old profiles while R56 writes only the richer window field.
func ApplyActualInputCostWindowObservation(previous ActualInputCostProfile, observation ActualInputCostObservation, nowMS int64) ActualInputCostProfile {
	if observation.Key == "" {
		return previous
	}
	if nowMS <= 0 {
		nowMS = observation.EventMS
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	if !previous.valid() {
		previous = ActualInputCostProfile{}
	}
	if ActualInputCostProfileHasObservation(previous, observation.ObservationID) {
		return previous
	}
	if observation.MissingEvidence {
		// Compatibility EWMA state owns missing-evidence accounting. The window
		// updater must not double-increment that streak.
		return previous
	}
	if !observation.Reliable || observation.TotalInputTokens <= 0 ||
		observation.RegularInputTokens < 0 || observation.CacheReadTokens < 0 ||
		observation.CacheCreation5mTokens < 0 || observation.CacheCreation1hTokens < 0 {
		return previous
	}
	remaining := observation.TotalInputTokens
	for _, component := range []int64{observation.RegularInputTokens, observation.CacheReadTokens, observation.CacheCreation5mTokens} {
		if component > remaining {
			return previous
		}
		remaining -= component
	}
	if observation.CacheCreation1hTokens != remaining {
		return previous
	}
	eventMS := observation.EventMS
	if eventMS <= 0 {
		eventMS = nowMS
	}
	if eventMS > nowMS {
		return previous
	}
	window := make([]ActualInputCostWindowSample, 0, minInt(actualInputCostWindowSize, len(previous.Window)+1))
	for _, sample := range previous.Window {
		if sample.EventMS <= 0 || sample.EventMS > nowMS || nowMS-sample.EventMS > actualInputCostWindowHorizonMS {
			continue
		}
		window = append(window, sample)
	}
	sample := ActualInputCostWindowSample{
		ObservationHash:    actualInputCostObservationHash(observation),
		RegularInputTokens: observation.RegularInputTokens, CacheReadTokens: observation.CacheReadTokens,
		CacheCreation5mTokens: observation.CacheCreation5mTokens, CacheCreation1hTokens: observation.CacheCreation1hTokens,
		TotalInputTokens: observation.TotalInputTokens, EventMS: eventMS,
		TieredExpressionHash:   strings.TrimSpace(observation.TieredExpressionHash),
		TieredInputPriceMicros: observation.TieredInputPriceMicros,
		Warmup:                 observation.Warmup,
	}
	window = append(window, sample)
	// Keep newest observations only. Sorting here is bounded at 101 items.
	sort.SliceStable(window, func(i, j int) bool { return window[i].EventMS > window[j].EventMS })
	if len(window) > actualInputCostWindowSize {
		window = window[:actualInputCostWindowSize]
	}
	previous.Window = window
	previous = appendActualInputCostHistoryObservation(previous, sample, nowMS)
	return previous
}

func appendActualInputCostHistoryObservation(
	profile ActualInputCostProfile,
	sample ActualInputCostWindowSample,
	nowMS int64,
) ActualInputCostProfile {
	if !validWindowSample(sample) {
		return profile
	}
	history := profile.History
	for _, pending := range history.Pending {
		if pending.ObservationHash != "" && pending.ObservationHash == sample.ObservationHash {
			return profile
		}
	}
	if history.PendingStartedMS <= 0 {
		history.PendingStartedMS = sample.EventMS
	}
	history.Pending = append(history.Pending, sample)
	shouldFlush := len(history.Pending) >= actualInputCostHistoryBatchSize ||
		(sample.EventMS-history.PendingStartedMS >= actualInputCostHistoryBatchIntervalMS)
	profile.History = history
	if shouldFlush {
		profile = flushActualInputCostHistory(profile, nowMS)
	}
	return profile
}

func flushActualInputCostHistory(profile ActualInputCostProfile, nowMS int64) ActualInputCostProfile {
	history := profile.History
	if len(history.Pending) == 0 {
		return profile
	}
	batch := append([]ActualInputCostWindowSample(nil), history.Pending...)
	regular, read, create5m, create1h, median, mad, lastEventMS, count, ok :=
		actualInputCostHistoryBatchMix(batch, nowMS)
	history.Pending = nil
	history.PendingStartedMS = 0
	if !ok || count <= 0 {
		profile.History = history
		return profile
	}
	alpha := 1 - math.Pow(2, -float64(count)/actualInputCostHistorySampleHalfLife)
	if alpha > actualInputCostHistoryAlphaMax {
		alpha = actualInputCostHistoryAlphaMax
	}
	if alpha <= 0 || math.IsNaN(alpha) || math.IsInf(alpha, 0) {
		profile.History = history
		return profile
	}
	if history.Version == actualInputCostHistoryVersion && history.TotalSamples > 0 {
		direction := 0
		band := 2 * history.MADCacheReadRate
		if read > history.MedianCacheReadRate+band {
			direction = 1
		} else if read < history.MedianCacheReadRate-band {
			direction = -1
		}
		if direction != 0 && direction == history.DriftDirection {
			history.ConsecutiveDriftBatches++
		} else if direction != 0 {
			history.DriftDirection = direction
			history.ConsecutiveDriftBatches = 1
		} else {
			history.DriftDirection = 0
			history.ConsecutiveDriftBatches = 0
		}
		if history.ConsecutiveDriftBatches >= 3 && alpha < actualInputCostHistoryDriftAlphaMax {
			alpha = actualInputCostHistoryDriftAlphaMax
		}
		history.RegularShare = history.RegularShare*(1-alpha) + regular*alpha
		history.CacheReadShare = history.CacheReadShare*(1-alpha) + read*alpha
		history.CacheCreation5mShare = history.CacheCreation5mShare*(1-alpha) + create5m*alpha
		history.CacheCreation1hShare = history.CacheCreation1hShare*(1-alpha) + create1h*alpha
		history.MedianCacheReadRate = history.MedianCacheReadRate*(1-alpha) + median*alpha
		history.MADCacheReadRate = history.MADCacheReadRate*(1-alpha) + mad*alpha
		history.EffectiveSamples = history.EffectiveSamples*(1-alpha) + float64(count)
	} else {
		history.Version = actualInputCostHistoryVersion
		history.RegularShare = regular
		history.CacheReadShare = read
		history.CacheCreation5mShare = create5m
		history.CacheCreation1hShare = create1h
		history.MedianCacheReadRate = median
		history.MADCacheReadRate = mad
		history.EffectiveSamples = float64(count)
		history.DriftDirection = 0
		history.ConsecutiveDriftBatches = 0
	}
	if total := history.RegularShare + history.CacheReadShare + history.CacheCreation5mShare + history.CacheCreation1hShare; total > 0 {
		history.RegularShare /= total
		history.CacheReadShare /= total
		history.CacheCreation5mShare /= total
		history.CacheCreation1hShare /= total
	}
	history.TotalSamples += int64(count)
	if lastEventMS > history.LastEventMS {
		history.LastEventMS = lastEventMS
	}
	profile.History = history
	profile = updateActualInputCostTieredHistoryBatch(profile, batch, nowMS)
	return profile
}

func actualInputCostHistoryBatchMix(
	samples []ActualInputCostWindowSample,
	nowMS int64,
) (regular, read, create5m, create1h, median, mad float64, lastEventMS int64, count int, ok bool) {
	valid := make([]ActualInputCostWindowSample, 0, len(samples))
	totals := make([]int64, 0, len(samples))
	rates := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if !validWindowSample(sample) || sample.EventMS > nowMS {
			continue
		}
		valid = append(valid, sample)
		totals = append(totals, sample.TotalInputTokens)
		rates = append(rates, float64(sample.CacheReadTokens)/float64(sample.TotalInputTokens))
	}
	count = len(valid)
	if count == 0 {
		return 0, 0, 0, 0, 0, 0, 0, 0, false
	}
	sort.Slice(totals, func(left, right int) bool { return totals[left] < totals[right] })
	tokenCap := totals[(90*len(totals)+99)/100-1]
	trimmed := trimWindowSamples(valid)
	var weightSum float64
	for _, sample := range trimmed {
		boundedTokens := sample.TotalInputTokens
		if boundedTokens > tokenCap {
			boundedTokens = tokenCap
		}
		ageMS := nowMS - sample.EventMS
		if ageMS < 0 {
			ageMS = 0
		}
		weight := float64(boundedTokens) * math.Pow(2, -float64(ageMS)/actualInputCostWindowHalfLifeMS)
		total := float64(sample.TotalInputTokens)
		regular += float64(sample.RegularInputTokens) / total * weight
		read += float64(sample.CacheReadTokens) / total * weight
		create5m += float64(sample.CacheCreation5mTokens) / total * weight
		create1h += float64(sample.CacheCreation1hTokens) / total * weight
		weightSum += weight
		if sample.EventMS > lastEventMS {
			lastEventMS = sample.EventMS
		}
	}
	if weightSum <= 0 {
		return 0, 0, 0, 0, 0, 0, 0, count, false
	}
	regular /= weightSum
	read /= weightSum
	create5m /= weightSum
	create1h /= weightSum
	sort.Float64s(rates)
	median = percentileFloat64(rates, 0.5)
	deviations := make([]float64, 0, len(rates))
	for _, value := range rates {
		deviations = append(deviations, math.Abs(value-median))
	}
	sort.Float64s(deviations)
	mad = percentileFloat64(deviations, 0.5)
	return regular, read, create5m, create1h, median, mad, lastEventMS, count, true
}

func percentileFloat64(sorted []float64, percentile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if percentile <= 0 {
		return sorted[0]
	}
	if percentile >= 1 {
		return sorted[len(sorted)-1]
	}
	position := percentile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	fraction := position - float64(lower)
	return sorted[lower]*(1-fraction) + sorted[upper]*fraction
}

func updateActualInputCostTieredHistoryBatch(
	profile ActualInputCostProfile,
	samples []ActualInputCostWindowSample,
	nowMS int64,
) ActualInputCostProfile {
	byExpression := make(map[string][]ActualInputCostWindowSample)
	for _, sample := range samples {
		hash := strings.TrimSpace(sample.TieredExpressionHash)
		if hash == "" || sample.TieredInputPriceMicros < 0 || sample.EventMS > nowMS {
			continue
		}
		byExpression[hash] = append(byExpression[hash], sample)
	}
	if len(byExpression) == 0 {
		return profile
	}
	if profile.TieredCostHistory == nil {
		profile.TieredCostHistory = make(map[string]ActualInputCostCostHistory)
	}
	for hash, expressionSamples := range byExpression {
		sort.SliceStable(expressionSamples, func(left, right int) bool {
			return expressionSamples[left].TieredInputPriceMicros < expressionSamples[right].TieredInputPriceMicros
		})
		if len(expressionSamples) >= 10 {
			trim := len(expressionSamples) / 10
			expressionSamples = expressionSamples[trim : len(expressionSamples)-trim]
		}
		var weightedCost float64
		var weightSum float64
		var lastEventMS int64
		for _, sample := range expressionSamples {
			ageMS := nowMS - sample.EventMS
			if ageMS < 0 {
				ageMS = 0
			}
			weight := math.Pow(2, -float64(ageMS)/actualInputCostWindowHalfLifeMS)
			weightedCost += float64(sample.TieredInputPriceMicros) * weight
			weightSum += weight
			if sample.EventMS > lastEventMS {
				lastEventMS = sample.EventMS
			}
		}
		if weightSum <= 0 {
			continue
		}
		batchMean := weightedCost / weightSum
		count := len(expressionSamples)
		alpha := 1 - math.Pow(2, -float64(count)/actualInputCostHistorySampleHalfLife)
		if alpha > actualInputCostHistoryAlphaMax {
			alpha = actualInputCostHistoryAlphaMax
		}
		history := profile.TieredCostHistory[hash]
		if history.TotalSamples <= 0 || history.EffectiveSamples <= 0 {
			history.ValueMicros = batchMean * float64(count)
			history.EffectiveSamples = float64(count)
		} else {
			oldMean := history.ValueMicros / history.EffectiveSamples
			newMean := oldMean*(1-alpha) + batchMean*alpha
			history.EffectiveSamples = history.EffectiveSamples*(1-alpha) + float64(count)
			history.ValueMicros = newMean * history.EffectiveSamples
		}
		history.TotalSamples += int64(count)
		if lastEventMS > history.LastEventMS {
			history.LastEventMS = lastEventMS
		}
		profile.TieredCostHistory[hash] = history
	}
	return profile
}

func actualInputCostObservationHash(observation ActualInputCostObservation) string {
	if id := strings.TrimSpace(observation.ObservationID); id != "" {
		return actualInputCostObservationDigest("observation", id)
	}
	// Older focused callers do not have an operational request id. A stable
	// value fingerprint still prevents duplicate replica merges without
	// changing the request-path learner's stronger idempotency contract.
	return actualInputCostObservationDigest(
		"observation-value",
		strconv.FormatInt(observation.EventMS, 10),
		strconv.FormatInt(observation.RegularInputTokens, 10),
		strconv.FormatInt(observation.CacheReadTokens, 10),
		strconv.FormatInt(observation.CacheCreation5mTokens, 10),
		strconv.FormatInt(observation.CacheCreation1hTokens, 10),
		strconv.FormatInt(observation.TotalInputTokens, 10),
		strings.TrimSpace(observation.TieredExpressionHash),
		strconv.FormatInt(observation.TieredInputPriceMicros, 10),
	)
}

// ActualInputCostProfileHasObservation makes process restarts and overlapping
// log replay idempotent before the legacy EWMA is updated. The comparison uses
// the same irreversible bounded hash persisted in the profile window.
func ActualInputCostProfileHasObservation(profile ActualInputCostProfile, observationID string) bool {
	observationID = strings.TrimSpace(observationID)
	if observationID == "" {
		return false
	}
	wanted := actualInputCostObservationDigest("observation", observationID)
	for _, sample := range profile.Window {
		if strings.TrimSpace(sample.ObservationHash) == wanted {
			return true
		}
	}
	return false
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func profileCacheMix(profile ActualInputCostProfile) (regular, read, create5m, create1h, total float64, ok bool) {
	if !profile.valid() {
		return 0, 0, 0, 0, 0, false
	}
	regular = profile.WeightedRegularTokens
	read = profile.WeightedCacheReadTokens
	create5m = profile.WeightedCacheCreation5m
	create1h = profile.WeightedCacheCreation1h
	total = profile.WeightedTotalInputTokens
	if regular < 0 || read < 0 || create5m < 0 || create1h < 0 || total <= 0 ||
		math.IsNaN(regular) || math.IsNaN(read) || math.IsNaN(create5m) || math.IsNaN(create1h) || math.IsNaN(total) {
		return 0, 0, 0, 0, 0, false
	}
	if regular+read+create5m+create1h > total*1.000001 {
		return 0, 0, 0, 0, 0, false
	}
	return regular, read, create5m, create1h, total, true
}

func validWindowSample(sample ActualInputCostWindowSample) bool {
	if sample.TotalInputTokens <= 0 || sample.RegularInputTokens < 0 || sample.CacheReadTokens < 0 ||
		sample.CacheCreation5mTokens < 0 || sample.CacheCreation1hTokens < 0 || sample.EventMS <= 0 ||
		sample.TieredInputPriceMicros < 0 {
		return false
	}
	if strings.TrimSpace(sample.TieredExpressionHash) == "" && sample.TieredInputPriceMicros != 0 {
		return false
	}
	remaining := sample.TotalInputTokens - sample.RegularInputTokens - sample.CacheReadTokens - sample.CacheCreation5mTokens
	return remaining == sample.CacheCreation1hTokens
}

func windowSamplesAt(profile ActualInputCostProfile, nowMS int64) []ActualInputCostWindowSample {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	result := make([]ActualInputCostWindowSample, 0, len(profile.Window))
	for _, sample := range profile.Window {
		if !validWindowSample(sample) || sample.EventMS > nowMS || nowMS-sample.EventMS > actualInputCostWindowHorizonMS {
			continue
		}
		result = append(result, sample)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].EventMS > result[j].EventMS })
	if len(result) > actualInputCostWindowSize {
		result = result[:actualInputCostWindowSize]
	}
	return result
}

func trimWindowSamples(samples []ActualInputCostWindowSample) []ActualInputCostWindowSample {
	if len(samples) < 10 {
		return samples
	}
	// Trim ten percent from both tails once there are enough request-level
	// observations to do so. Sparse evidence remains fully visible and is
	// represented through confidence, never through synthetic samples.
	type rankedSample struct {
		sample ActualInputCostWindowSample
		rate   float64
		index  int
	}
	ranked := make([]rankedSample, 0, len(samples))
	for index, sample := range samples {
		rate := float64(sample.CacheReadTokens) / float64(sample.TotalInputTokens)
		ranked = append(ranked, rankedSample{sample: sample, rate: rate, index: index})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].rate != ranked[j].rate {
			return ranked[i].rate < ranked[j].rate
		}
		return ranked[i].index < ranked[j].index
	})
	trimEachSide := len(ranked) / 10
	if trimEachSide == 0 || trimEachSide*2 >= len(ranked) {
		return samples
	}
	trimmed := ranked[trimEachSide : len(ranked)-trimEachSide]
	result := make([]ActualInputCostWindowSample, 0, len(trimmed))
	for _, entry := range trimmed {
		result = append(result, entry.sample)
	}
	return result
}

func tieredCostSamplesAt(profile ActualInputCostProfile, expressionHash string, nowMS int64) []ActualInputCostWindowSample {
	expressionHash = strings.TrimSpace(expressionHash)
	if expressionHash == "" {
		return nil
	}
	samples := windowSamplesAt(profile, nowMS)
	result := make([]ActualInputCostWindowSample, 0, len(samples))
	for _, sample := range samples {
		if strings.TrimSpace(sample.TieredExpressionHash) != expressionHash {
			continue
		}
		result = append(result, sample)
	}
	return result
}

func trimTieredCostSamples(samples []ActualInputCostWindowSample) []ActualInputCostWindowSample {
	if len(samples) < 10 {
		return samples
	}
	result := append([]ActualInputCostWindowSample(nil), samples...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].TieredInputPriceMicros != result[j].TieredInputPriceMicros {
			return result[i].TieredInputPriceMicros < result[j].TieredInputPriceMicros
		}
		if result[i].EventMS != result[j].EventMS {
			return result[i].EventMS > result[j].EventMS
		}
		return result[i].ObservationHash < result[j].ObservationHash
	})
	trimEachSide := len(result) / 10
	if trimEachSide == 0 || trimEachSide*2 >= len(result) {
		return samples
	}
	return result[trimEachSide : len(result)-trimEachSide]
}

func tieredInputPriceMicrosAt(profile ActualInputCostProfile, expressionHash string, nowMS int64) (
	priceMicros float64,
	lastEventMS int64,
	sampleCount int,
	ok bool,
) {
	samples := trimTieredCostSamples(tieredCostSamplesAt(profile, expressionHash, nowMS))
	if len(samples) == 0 {
		return 0, 0, 0, false
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	var weightedTotal, weightTotal float64
	for _, sample := range samples {
		ageMS := nowMS - sample.EventMS
		if ageMS < 0 {
			ageMS = 0
		}
		weight := math.Pow(2, -float64(ageMS)/actualInputCostWindowHalfLifeMS)
		weightedTotal += float64(sample.TieredInputPriceMicros) * weight
		weightTotal += weight
		if sample.EventMS > lastEventMS {
			lastEventMS = sample.EventMS
		}
	}
	if weightTotal <= 0 || math.IsNaN(weightTotal) || math.IsInf(weightTotal, 0) {
		return 0, 0, 0, false
	}
	priceMicros = weightedTotal / weightTotal
	if priceMicros < 0 || math.IsNaN(priceMicros) || math.IsInf(priceMicros, 0) {
		return 0, 0, 0, false
	}
	return priceMicros, lastEventMS, len(samples), true
}

func tieredInputPriceHistoryMicros(profile ActualInputCostProfile, expressionHash string) (
	priceMicros float64,
	lastEventMS int64,
	sampleCount int64,
	ok bool,
) {
	history, exists := profile.TieredCostHistory[strings.TrimSpace(expressionHash)]
	if !exists || history.EffectiveSamples <= 0 || history.TotalSamples <= 0 || history.LastEventMS <= 0 {
		return 0, 0, 0, false
	}
	priceMicros = history.ValueMicros / history.EffectiveSamples
	if priceMicros < 0 || math.IsNaN(priceMicros) || math.IsInf(priceMicros, 0) {
		return 0, 0, 0, false
	}
	return priceMicros, history.LastEventMS, history.TotalSamples, true
}

func windowMixAt(profile ActualInputCostProfile, nowMS int64, fillPrior bool) (
	regular, read, create5m, create1h, observedReadRate float64,
	lastEventMS int64, sampleCount int, ok bool,
) {
	rawSamples := windowSamplesAt(profile, nowMS)
	sampleCount = len(rawSamples)
	samples := trimWindowSamples(rawSamples)
	if sampleCount == 0 && !fillPrior {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	weight := func(ageMS int64) float64 {
		if ageMS < 0 {
			ageMS = 0
		}
		return math.Pow(2, -float64(ageMS)/actualInputCostWindowHalfLifeMS)
	}
	tokenCap := int64(0)
	if len(samples) >= 10 {
		totals := make([]int64, 0, len(samples))
		for _, sample := range samples {
			totals = append(totals, sample.TotalInputTokens)
		}
		sort.Slice(totals, func(left, right int) bool { return totals[left] < totals[right] })
		tokenCap = totals[(90*len(totals)+99)/100-1]
	}
	for _, sample := range samples {
		w := weight(nowMS - sample.EventMS)
		if tokenCap > 0 {
			tokenWeight := sample.TotalInputTokens
			if tokenWeight > tokenCap {
				tokenWeight = tokenCap
			}
			w *= float64(tokenWeight)
		}
		total := float64(sample.TotalInputTokens)
		regular += float64(sample.RegularInputTokens) / total * w
		read += float64(sample.CacheReadTokens) / total * w
		create5m += float64(sample.CacheCreation5mTokens) / total * w
		create1h += float64(sample.CacheCreation1hTokens) / total * w
		if sample.EventMS > lastEventMS {
			lastEventMS = sample.EventMS
		}
	}
	if len(samples) > 0 {
		observedTotal := regular + read + create5m + create1h
		if observedTotal > 0 {
			observedReadRate = read / observedTotal * float64(actualInputCostPPMScale)
		}
	}
	recentTotal := regular + read + create5m + create1h
	if recentTotal > 0 {
		regular /= recentTotal
		read /= recentTotal
		create5m /= recentTotal
		create1h /= recentTotal
	}
	if fillPrior {
		historyRegular, historyRead, historyCreate5m, historyCreate1h, historyOK := actualInputCostHistoryMix(profile)
		if !historyOK {
			historyRegular, historyRead = 0.1, 0.9
		}
		recentShare := float64(sampleCount) / float64(actualInputCostWindowSize)
		if recentShare > 1 {
			recentShare = 1
		}
		if recentTotal <= 0 {
			recentShare = 0
		}
		historyShare := 1 - recentShare
		regular = regular*recentShare + historyRegular*historyShare
		read = read*recentShare + historyRead*historyShare
		create5m = create5m*recentShare + historyCreate5m*historyShare
		create1h = create1h*recentShare + historyCreate1h*historyShare
	}
	total := regular + read + create5m + create1h
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, 0, 0, 0, 0, lastEventMS, sampleCount, false
	}
	return regular / total, read / total, create5m / total, create1h / total,
		observedReadRate, lastEventMS, sampleCount, true
}

func actualInputCostHistoryMix(profile ActualInputCostProfile) (
	regular float64,
	read float64,
	create5m float64,
	create1h float64,
	ok bool,
) {
	history := profile.History
	if history.Version != actualInputCostHistoryVersion || history.TotalSamples <= 0 ||
		history.EffectiveSamples <= 0 || history.LastEventMS <= 0 {
		return 0, 0, 0, 0, false
	}
	regular = history.RegularShare
	read = history.CacheReadShare
	create5m = history.CacheCreation5mShare
	create1h = history.CacheCreation1hShare
	total := regular + read + create5m + create1h
	if regular < 0 || read < 0 || create5m < 0 || create1h < 0 || total <= 0 ||
		math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, 0, 0, 0, false
	}
	return regular / total, read / total, create5m / total, create1h / total, true
}

func mergeActualInputCostWindows(profiles ...ActualInputCostProfile) []ActualInputCostWindowSample {
	byObservation := make(map[string]ActualInputCostWindowSample)
	for _, profile := range profiles {
		for _, sample := range profile.Window {
			if !validWindowSample(sample) {
				continue
			}
			identity := strings.TrimSpace(sample.ObservationHash)
			if identity == "" {
				identity = actualInputCostObservationDigest(
					"legacy-window-sample",
					strconv.FormatInt(sample.EventMS, 10),
					strconv.FormatInt(sample.RegularInputTokens, 10),
					strconv.FormatInt(sample.CacheReadTokens, 10),
					strconv.FormatInt(sample.CacheCreation5mTokens, 10),
					strconv.FormatInt(sample.CacheCreation1hTokens, 10),
					strconv.FormatInt(sample.TotalInputTokens, 10),
					strings.TrimSpace(sample.TieredExpressionHash),
					strconv.FormatInt(sample.TieredInputPriceMicros, 10),
				)
			}
			current, exists := byObservation[identity]
			if !exists || sample.EventMS > current.EventMS {
				sample.ObservationHash = identity
				byObservation[identity] = sample
			}
		}
	}
	window := make([]ActualInputCostWindowSample, 0, len(byObservation))
	for _, sample := range byObservation {
		window = append(window, sample)
	}
	sort.SliceStable(window, func(i, j int) bool {
		if window[i].EventMS != window[j].EventMS {
			return window[i].EventMS > window[j].EventMS
		}
		return window[i].ObservationHash < window[j].ObservationHash
	})
	if len(window) > actualInputCostWindowSize {
		window = window[:actualInputCostWindowSize]
	}
	return window
}

func mergeActualInputCostHistory(profiles ...ActualInputCostProfile) ActualInputCostHistorySummary {
	selected := ActualInputCostHistorySummary{}
	for _, profile := range profiles {
		candidate := profile.History
		if candidate.Version != actualInputCostHistoryVersion && candidate.TotalSamples > 0 {
			continue
		}
		if candidate.TotalSamples > selected.TotalSamples ||
			(candidate.TotalSamples == selected.TotalSamples && candidate.LastEventMS > selected.LastEventMS) {
			selected = candidate
		}
	}
	byObservation := make(map[string]ActualInputCostWindowSample)
	for _, profile := range profiles {
		for _, sample := range profile.History.Pending {
			if !validWindowSample(sample) {
				continue
			}
			identity := strings.TrimSpace(sample.ObservationHash)
			if identity == "" {
				identity = actualInputCostObservationDigest(
					"legacy-history-pending",
					strconv.FormatInt(sample.EventMS, 10),
					strconv.FormatInt(sample.RegularInputTokens, 10),
					strconv.FormatInt(sample.CacheReadTokens, 10),
					strconv.FormatInt(sample.CacheCreation5mTokens, 10),
					strconv.FormatInt(sample.CacheCreation1hTokens, 10),
					strconv.FormatInt(sample.TotalInputTokens, 10),
				)
			}
			sample.ObservationHash = identity
			current, exists := byObservation[identity]
			if !exists || sample.EventMS > current.EventMS {
				byObservation[identity] = sample
			}
		}
	}
	selected.Pending = make([]ActualInputCostWindowSample, 0, len(byObservation))
	for _, sample := range byObservation {
		selected.Pending = append(selected.Pending, sample)
	}
	sort.SliceStable(selected.Pending, func(left, right int) bool {
		if selected.Pending[left].EventMS != selected.Pending[right].EventMS {
			return selected.Pending[left].EventMS < selected.Pending[right].EventMS
		}
		return selected.Pending[left].ObservationHash < selected.Pending[right].ObservationHash
	})
	if len(selected.Pending) > 0 {
		selected.PendingStartedMS = selected.Pending[0].EventMS
	} else {
		selected.PendingStartedMS = 0
	}
	return selected
}

func mergeActualInputCostTieredHistory(profiles ...ActualInputCostProfile) map[string]ActualInputCostCostHistory {
	merged := make(map[string]ActualInputCostCostHistory)
	for _, profile := range profiles {
		for hash, candidate := range profile.TieredCostHistory {
			hash = strings.TrimSpace(hash)
			if hash == "" || candidate.TotalSamples <= 0 || candidate.EffectiveSamples <= 0 {
				continue
			}
			current, exists := merged[hash]
			if !exists || candidate.TotalSamples > current.TotalSamples ||
				(candidate.TotalSamples == current.TotalSamples && candidate.LastEventMS > current.LastEventMS) {
				merged[hash] = candidate
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// MergeActualInputCostProfiles combines independent exact profiles without
// changing their weighting. Invalid profiles are ignored, so observability can
// disappear safely without affecting route ordering or request handling.
func MergeActualInputCostProfiles(profiles ...ActualInputCostProfile) (ActualInputCostProfile, bool) {
	if window := mergeActualInputCostWindows(profiles...); len(window) > 0 {
		merged := ActualInputCostProfile{
			Version:           ActualInputCostFeatureVersion,
			Window:            window,
			History:           mergeActualInputCostHistory(profiles...),
			TieredCostHistory: mergeActualInputCostTieredHistory(profiles...),
		}
		for _, sample := range window {
			merged.WeightedRegularTokens += float64(sample.RegularInputTokens)
			merged.WeightedCacheReadTokens += float64(sample.CacheReadTokens)
			merged.WeightedCacheCreation5m += float64(sample.CacheCreation5mTokens)
			merged.WeightedCacheCreation1h += float64(sample.CacheCreation1hTokens)
			merged.WeightedTotalInputTokens += float64(sample.TotalInputTokens)
			merged.WeightedSamples++
			merged.TotalSamples++
			if sample.EventMS > merged.LastEventMS {
				merged.LastEventMS = sample.EventMS
			}
		}
		if _, _, _, _, _, ok := profileCacheMix(merged); !ok {
			return ActualInputCostProfile{}, false
		}
		if len(merged.History.Pending) >= actualInputCostHistoryBatchSize {
			merged = flushActualInputCostHistory(merged, merged.LastEventMS)
		}
		return merged, true
	}
	merged := ActualInputCostProfile{Version: ActualInputCostFeatureVersion}
	found := false
	for _, profile := range profiles {
		if _, _, _, _, _, ok := profileCacheMix(profile); !ok {
			continue
		}
		merged.WeightedRegularTokens += profile.WeightedRegularTokens
		merged.WeightedCacheReadTokens += profile.WeightedCacheReadTokens
		merged.WeightedCacheCreation5m += profile.WeightedCacheCreation5m
		merged.WeightedCacheCreation1h += profile.WeightedCacheCreation1h
		merged.WeightedTotalInputTokens += profile.WeightedTotalInputTokens
		merged.WeightedSamples += profile.WeightedSamples
		if profile.TotalSamples > math.MaxInt64-merged.TotalSamples {
			merged.TotalSamples = math.MaxInt64
		} else {
			merged.TotalSamples += profile.TotalSamples
		}
		if profile.LastEventMS > merged.LastEventMS {
			merged.LastEventMS = profile.LastEventMS
		}
		found = true
	}
	if !found {
		return ActualInputCostProfile{}, false
	}
	if _, _, _, _, _, ok := profileCacheMix(merged); !ok {
		return ActualInputCostProfile{}, false
	}
	return merged, true
}

// ActualInputCostCacheReadRatePPM exposes the same normalized cache-read share
// used by EstimateActualInputCost. A reliable zero-hit profile returns (0,
// true); missing or malformed evidence returns false.
func ActualInputCostCacheReadRatePPM(profile ActualInputCostProfile) (int64, bool) {
	regular, read, create5m, create1h, _, ok := profileCacheMix(profile)
	if !ok {
		return 0, false
	}
	total := regular + read + create5m + create1h
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, false
	}
	rate := math.Round(read / total * float64(actualInputCostPPMScale))
	if rate < 0 || rate > float64(actualInputCostPPMScale) || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	return int64(rate), true
}

// ActualInputCostCacheReadRatePPMAt applies the same bounded one-hour,
// per-request trimming and recency weighting used by route ordering. The
// legacy helper above remains for old EWMA-only profiles and compatibility.
func ActualInputCostCacheReadRatePPMAt(profile ActualInputCostProfile, nowMS int64) (int64, int64, int64, bool) {
	if len(profile.Window) > 0 {
		_, read, _, _, _, lastEventMS, samples, ok := windowMixAt(profile, nowMS, false)
		if !ok || samples == 0 {
			return 0, 0, 0, false
		}
		rate := math.Round(read * float64(actualInputCostPPMScale))
		if rate < 0 || rate > float64(actualInputCostPPMScale) || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return 0, 0, 0, false
		}
		return int64(rate), int64(samples), lastEventMS, true
	}
	rate, ok := ActualInputCostCacheReadRatePPM(profile)
	if !ok {
		return 0, 0, 0, false
	}
	return rate, profile.TotalSamples, profile.LastEventMS, true
}

// ActualInputCostEffectiveCacheReadRatePPMAt returns the real observed cache
// read rate and the continuously aged rate used by R42 ordering. It requires
// real evidence; the hidden cold-start prior is never exposed through this
// visibility helper.
func ActualInputCostEffectiveCacheReadRatePPMAt(profile ActualInputCostProfile, nowMS int64) (
	effectivePPM int64,
	observedPPM int64,
	evidenceAgeMS int64,
	ok bool,
) {
	if len(profile.Window) > 0 {
		_, read, _, _, observed, lastEventMS, samples, ok := windowMixAt(profile, nowMS, true)
		if !ok || samples == 0 {
			return 0, 0, 0, false
		}
		effective := int64(math.Round(read * float64(actualInputCostPPMScale)))
		return effective, int64(math.Round(observed)), nowMS - lastEventMS, true
	}
	if profile.MissingEvidenceStreak > 0 {
		return 0, 0, 0, false
	}
	_, read, _, _, observedReadRate, effectiveReadRate, ageMS, valid := actualInputCostAgedMixAt(profile, nowMS)
	if !valid || read < 0 {
		return 0, 0, 0, false
	}
	return effectiveReadRate, observedReadRate, ageMS, true
}

// ActualInputCostAgedProfileAt projects one real exact profile to a stable
// read time while retaining its token weight for later aggregation. Profiles
// in a missing-evidence retry/cooldown state are deliberately hidden: route
// ordering is using an undisplayed optimistic or static fallback rather than
// those stale measurements.
func ActualInputCostAgedProfileAt(profile ActualInputCostProfile, nowMS int64) (ActualInputCostProfile, bool) {
	if len(profile.Window) > 0 {
		window := windowSamplesAt(profile, nowMS)
		if len(window) == 0 {
			return ActualInputCostProfile{}, false
		}
		agedWindow := profile
		agedWindow.Window = window
		regular, read, create5m, create1h, _, lastEventMS, samples, ok := windowMixAt(agedWindow, nowMS, false)
		if !ok || samples == 0 {
			return ActualInputCostProfile{}, false
		}
		return ActualInputCostProfile{
			Version: profile.Version, WeightedRegularTokens: regular, WeightedCacheReadTokens: read,
			WeightedCacheCreation5m: create5m, WeightedCacheCreation1h: create1h,
			WeightedTotalInputTokens: 1, WeightedSamples: float64(samples), TotalSamples: int64(samples), LastEventMS: lastEventMS,
			Window: window,
		}, true
	}
	if profile.MissingEvidenceStreak > 0 {
		return ActualInputCostProfile{}, false
	}
	regular, read, create5m, create1h, _, _, _, ok := actualInputCostAgedMixAt(profile, nowMS)
	if !ok {
		return ActualInputCostProfile{}, false
	}
	_, _, _, _, total, ok := profileCacheMix(profile)
	if !ok || total <= 0 {
		return ActualInputCostProfile{}, false
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	return ActualInputCostProfile{
		Version:                  ActualInputCostFeatureVersion,
		WeightedRegularTokens:    regular * total,
		WeightedCacheReadTokens:  read * total,
		WeightedCacheCreation5m:  create5m * total,
		WeightedCacheCreation1h:  create1h * total,
		WeightedTotalInputTokens: total,
		WeightedSamples:          profile.WeightedSamples,
		TotalSamples:             profile.TotalSamples,
		LastEventMS:              nowMS,
	}, true
}

// EstimateActualInputCost computes a current effective input price from a
// static route descriptor and learned cache mix.  Billing never calls this
// function; it is solely an ordering metric.
func EstimateActualInputCost(
	price RoutePrice,
	staticScorePPM int64,
	key string,
	poolKey string,
	snapshot ActualInputCostSnapshot,
) ActualInputCostEstimate {
	return EstimateActualInputCostAt(price, staticScorePPM, key, poolKey, snapshot, time.Now().UnixMilli())
}

// EstimateActualInputCostAt returns the ordering-only effective input cost at
// one stable planning time. An absent exact profile is treated as a cold-start
// profile and uses the established cache-rate prior; it must not disable the
// single price formula or make a usable route disappear from ranking.
func EstimateActualInputCostAt(
	price RoutePrice,
	staticScorePPM int64,
	key string,
	poolKey string,
	snapshot ActualInputCostSnapshot,
	nowMS int64,
) ActualInputCostEstimate {
	fallback := ActualInputCostEstimate{
		EffectivePPM:     staticScorePPM,
		Source:           ActualInputCostStaticFallback,
		Comparable:       staticScorePPM >= 0 && SupportsActualInputCost(price),
		CostSource:       ActualInputCostCostSourceUnavailable,
		EstimatorVersion: ActualInputCostEstimatorVersion,
	}
	// A missing or malformed price must not hide real cache facts. Cost
	// comparability is decided only after the evidence window has been read.
	if key == "" || !SupportsActualInputCost(price) {
		return fallback
	}
	if snapshot == nil {
		snapshot = make(ActualInputCostSnapshot)
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	_ = poolKey // Kept in the compatibility signature; cross-namespace priors no longer rank routes.

	exact := snapshot[key]
	if exact.Version != "" && !exact.valid() {
		return fallback
	}
	if exact.MissingEvidenceStreak < 0 || exact.LastMissingEvidenceMS < 0 ||
		(exact.MissingEvidenceStreak == 0 && exact.LastMissingEvidenceMS != 0) ||
		(exact.MissingEvidenceStreak > 0 && exact.LastMissingEvidenceMS == 0) {
		return fallback
	}
	priorRead := float64(actualInputCostOptimisticCacheReadPPM) / float64(actualInputCostPPMScale)
	regular, read, create5m, create1h := 1-priorRead, priorRead, 0.0, 0.0
	historyRegular, historyRead, historyCreate5m, historyCreate1h, historyAvailable := actualInputCostHistoryMix(exact)
	if historyAvailable {
		historicalPPM := int64(math.Round(historyRead * float64(actualInputCostPPMScale)))
		fallback.HistoricalCacheReadRatePPM = &historicalPPM
		fallback.HistoricalSamples = exact.History.TotalSamples
	}
	missingEvidence := exact.MissingEvidenceStreak > 0
	if missingEvidence && actualInputCostMissingEvidenceBlocked(exact, nowMS) {
		fallback.Source = ActualInputCostUnobservable
		return fallback
	}
	if missingEvidence {
		fallback.Source = ActualInputCostOptimistic
		fallback.Optimistic = true
	} else if len(exact.Window) > 0 {
		windowRegular, windowRead, windowCreate5m, windowCreate1h, observedReadRate, lastEventMS, sampleCount, windowValid := windowMixAt(exact, nowMS, true)
		if windowValid {
			regular, read, create5m, create1h = windowRegular, windowRead, windowCreate5m, windowCreate1h
			if sampleCount > 0 {
				fallback.Source = ActualInputCostObserved
				fallback.Samples = int64(sampleCount)
				fallback.LastObservedAtMS = lastEventMS
				fallback.EvidenceAgeMS = nowMS - lastEventMS
				fallback.HasRealEvidence = true
				observedPPM := int64(math.Round(observedReadRate))
				fallback.ObservedCacheReadRatePPM = &observedPPM
				fallback.ConfidencePPM = int64(sampleCount) * actualInputCostPPMScale / actualInputCostWindowSize
			} else if historyAvailable {
				fallback.Source = ActualInputCostHistorical
				fallback.LastObservedAtMS = exact.History.LastEventMS
				fallback.HasRealEvidence = true
			} else {
				fallback.Source = ActualInputCostOptimistic
				fallback.Optimistic = true
			}
		} else {
			fallback.Source = ActualInputCostOptimistic
			fallback.Optimistic = true
		}
	} else {
		if historyAvailable {
			regular, read = historyRegular, historyRead
			create5m, create1h = historyCreate5m, historyCreate1h
			fallback.Source = ActualInputCostHistorical
			fallback.LastObservedAtMS = exact.History.LastEventMS
			fallback.HasRealEvidence = true
		}
		agedRegular, agedRead, agedCreate5m, agedCreate1h, observedReadRate, _, ageMS, exactValid := actualInputCostAgedMixAt(exact, nowMS)
		if !historyAvailable && exactValid {
			regular, read = agedRegular, agedRead
			create5m, create1h = agedCreate5m, agedCreate1h
			fallback.Source = ActualInputCostObserved
			fallback.Samples = exact.TotalSamples
			fallback.LastObservedAtMS = exact.LastEventMS
			fallback.EvidenceAgeMS = ageMS
			fallback.HasRealEvidence = true
			fallback.ObservedCacheReadRatePPM = &observedReadRate
		} else if !historyAvailable {
			if actualInputCostProfileHasTokenState(exact) && !exactValid {
				legacyExpired := exact.valid() && exact.TotalSamples > 0 && exact.LastEventMS > 0 &&
					exact.LastEventMS <= nowMS && nowMS-exact.LastEventMS > actualInputCostWindowHorizonMS
				if !legacyExpired {
					return fallback
				}
			}
			fallback.Source = ActualInputCostOptimistic
			fallback.Optimistic = true
		}
	}

	total := regular + read + create5m + create1h
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return fallback
	}
	cacheReadRate := math.Round(read / total * float64(actualInputCostPPMScale))
	if cacheReadRate < 0 || cacheReadRate > float64(actualInputCostPPMScale) || math.IsNaN(cacheReadRate) || math.IsInf(cacheReadRate, 0) {
		return fallback
	}
	fallback.CacheReadRatePPM = int64(cacheReadRate)
	fallback.Comparable = false
	if staticScorePPM < 0 {
		return fallback
	}

	// Apply the same two-price formula for every token route. Tiered billing's
	// expression is only used to obtain the model's base/cache prices; history
	// never changes those prices and settlement remains authoritative for
	// accounting.
	value, ok := actualInputCostPPMForMix(price, staticScorePPM, regular, read, create5m, create1h)
	if ok {
		fallback.CostSource = ActualInputCostCostSourceRatio
		fallback.CostSamples = fallback.Samples
	}
	if !ok {
		return fallback
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt64) {
		return fallback
	}
	if value < 0 {
		return fallback
	}
	fallback.EffectivePPM = int64(math.Round(value))
	fallback.Comparable = true
	return fallback
}

// SupportsActualInputCost reports whether a route price can transform a
// measured input token mix into the common quota-per-million ordering unit.
func SupportsActualInputCost(price RoutePrice) bool {
	if !price.StaticComparable || price.ActualInputComparisonClass == "" || price.ScorePPM < 0 || price.BillingUnit != "per_token" {
		return false
	}
	switch price.BillingMode {
	case "ratio":
		return price.CachePricingKnown
	case "tiered_expr":
		_, _, ok := tieredInputBaseAndCacheUSD(price)
		return ok
	default:
		return false
	}
}

// ActualInputCostPPMForTokenMix projects one real request's normalized input
// mix through its frozen route-price contract. It is shared by the live
// estimator and the read-only analytics pipeline so historical charts do not
// invent a second cost formula. The result is a per-million ordering unit, not
// a supplier invoice or a billing input.
func ActualInputCostPPMForTokenMix(
	price RoutePrice,
	staticScorePPM int64,
	regularInputTokens int64,
	cacheReadTokens int64,
	cacheCreation5mTokens int64,
	cacheCreation1hTokens int64,
) (int64, bool) {
	if regularInputTokens < 0 || cacheReadTokens < 0 || cacheCreation5mTokens < 0 || cacheCreation1hTokens < 0 {
		return 0, false
	}
	value, ok := actualInputCostPPMForMix(
		price,
		staticScorePPM,
		float64(regularInputTokens),
		float64(cacheReadTokens),
		float64(cacheCreation5mTokens),
		float64(cacheCreation1hTokens),
	)
	if !ok || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value > float64(math.MaxInt64) {
		return 0, false
	}
	return int64(math.Round(value)), true
}

// ActualInputCostPPMForCacheReadRate applies the same unified two-price
// formula to an already-computed cache-read rate. The rate may come from the
// cache estimator's prior/history/time-factor chain; no cost-history value is
// introduced here.
func ActualInputCostPPMForCacheReadRate(price RoutePrice, staticScorePPM, cacheReadRatePPM int64) (int64, bool) {
	if cacheReadRatePPM < 0 || cacheReadRatePPM > actualInputCostPPMScale {
		return 0, false
	}
	value, ok := actualInputCostPPMForMix(
		price, staticScorePPM,
		float64(actualInputCostPPMScale-cacheReadRatePPM),
		float64(cacheReadRatePPM), 0, 0,
	)
	if !ok || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(math.Round(value)), true
}

// ActualInputCostObservedTokenMixForProfilesAt unions current request-level
// windows from independent replicas before calculating the observed mix. It
// is intentionally an evidence helper; prediction still goes through the
// cache estimator and the single cost formula.
func ActualInputCostObservedTokenMixForProfilesAt(profiles []ActualInputCostProfile, nowMS int64) (
	regular, read, create5m, create1h float64, samples int64, lastEventMS int64, ok bool,
) {
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	byObservation := make(map[string]ActualInputCostWindowSample)
	for _, profile := range profiles {
		for _, sample := range profile.Window {
			if !validWindowSample(sample) || sample.EventMS > nowMS || nowMS-sample.EventMS > actualInputCostWindowHorizonMS {
				continue
			}
			identity := strings.TrimSpace(sample.ObservationHash)
			if identity == "" {
				identity = actualInputCostObservationDigest("legacy-window-sample",
					strconv.FormatInt(sample.EventMS, 10), strconv.FormatInt(sample.RegularInputTokens, 10),
					strconv.FormatInt(sample.CacheReadTokens, 10), strconv.FormatInt(sample.CacheCreation5mTokens, 10),
					strconv.FormatInt(sample.CacheCreation1hTokens, 10), strconv.FormatInt(sample.TotalInputTokens, 10),
					strings.TrimSpace(sample.TieredExpressionHash), strconv.FormatInt(sample.TieredInputPriceMicros, 10))
			}
			current, exists := byObservation[identity]
			if !exists || sample.EventMS > current.EventMS {
				sample.ObservationHash = identity
				byObservation[identity] = sample
			}
		}
	}
	for _, sample := range byObservation {
		regular += float64(sample.RegularInputTokens)
		read += float64(sample.CacheReadTokens)
		create5m += float64(sample.CacheCreation5mTokens)
		create1h += float64(sample.CacheCreation1hTokens)
		samples++
		if sample.EventMS > lastEventMS {
			lastEventMS = sample.EventMS
		}
	}
	if samples == 0 || regular+read+create5m+create1h <= 0 {
		return 0, 0, 0, 0, 0, 0, false
	}
	return regular, read, create5m, create1h, samples, lastEventMS, true
}

func actualInputCostPPMForMix(
	price RoutePrice,
	staticScorePPM int64,
	regular float64,
	read float64,
	create5m float64,
	create1h float64,
) (float64, bool) {
	total := regular + read + create5m + create1h
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return 0, false
	}
	if price.BillingMode == "ratio" {
		cacheReadRatio := float64(price.CacheReadRatioPPM) / float64(actualInputCostPPMScale)
		create5mRatio := float64(price.CacheCreation5mRatioPPM) / float64(actualInputCostPPMScale)
		create1hRatio := float64(price.CacheCreation1hRatioPPM) / float64(actualInputCostPPMScale)
		coefficient := (regular + read*cacheReadRatio + create5m*create5mRatio + create1h*create1hRatio) / total
		if coefficient < 0 || math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
			return 0, false
		}
		return float64(staticScorePPM) * coefficient, true
	}
	if price.BillingMode == "tiered_expr" {
		baseUSD, cacheUSD, ok := tieredInputBaseAndCacheUSD(price)
		if !ok || staticScorePPM < 0 {
			return 0, false
		}
		// ScorePPM is the group ratio for tiered descriptors. Convert the
		// exact two-price USD result back to the common 2 USD/ratio-unit PPM
		// ordering scale used by the rest of Smart Router.
		groupRatio := float64(staticScorePPM) / float64(actualInputCostPPMScale)
		effectiveUSDPerMillion := groupRatio * (baseUSD*(regular+create5m+create1h) + cacheUSD*read) / total
		value := effectiveUSDPerMillion * float64(actualInputCostPPMScale) / 2.0
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
		return value, true
	}
	return 0, false
}

// tieredInputBaseAndCacheUSD extracts the two prices required by the unified
// input-cost formula from every configured tier in a billing expression. The
// prediction contract deliberately uses the cheapest ordinary-input and
// cache-read prices; request settlement continues to evaluate its frozen
// expression and is not changed by this helper.
func tieredInputBaseAndCacheUSD(price RoutePrice) (float64, float64, bool) {
	if !price.TieredInputPredictable || strings.TrimSpace(price.TieredExpression) == "" {
		return 0, 0, false
	}
	return cheapestTierInputPrices(price.TieredExpression, price.TieredExpressionHash)
}

func actualInputCostAgedMixAt(profile ActualInputCostProfile, nowMS int64) (
	regular float64,
	read float64,
	create5m float64,
	create1h float64,
	observedReadRatePPM int64,
	effectiveReadRatePPM int64,
	evidenceAgeMS int64,
	ok bool,
) {
	regular, read, create5m, create1h, _, valid := profileCacheMix(profile)
	if !valid || profile.TotalSamples <= 0 || profile.LastEventMS <= 0 {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	observedTotal := regular + read + create5m + create1h
	if observedTotal <= 0 || math.IsNaN(observedTotal) || math.IsInf(observedTotal, 0) {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	regular /= observedTotal
	read /= observedTotal
	create5m /= observedTotal
	create1h /= observedTotal
	observedReadRate := math.Round(read * float64(actualInputCostPPMScale))
	if observedReadRate < 0 || observedReadRate > float64(actualInputCostPPMScale) ||
		math.IsNaN(observedReadRate) || math.IsInf(observedReadRate, 0) {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}
	if profile.LastEventMS > nowMS {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	ageMS := nowMS - profile.LastEventMS
	if ageMS > actualInputCostWindowHorizonMS {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	effectiveTotal := regular + read + create5m + create1h
	if effectiveTotal <= 0 || math.IsNaN(effectiveTotal) || math.IsInf(effectiveTotal, 0) {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	effectiveReadRate := math.Round(read / effectiveTotal * float64(actualInputCostPPMScale))
	if effectiveReadRate < 0 || effectiveReadRate > float64(actualInputCostPPMScale) ||
		math.IsNaN(effectiveReadRate) || math.IsInf(effectiveReadRate, 0) {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	return regular, read, create5m, create1h, int64(observedReadRate), int64(effectiveReadRate), ageMS, true
}

func actualInputCostProfileHasTokenState(profile ActualInputCostProfile) bool {
	return profile.TotalSamples != 0 || profile.LastEventMS != 0 ||
		profile.WeightedRegularTokens != 0 || profile.WeightedCacheReadTokens != 0 ||
		profile.WeightedCacheCreation5m != 0 || profile.WeightedCacheCreation1h != 0 ||
		profile.WeightedTotalInputTokens != 0 || profile.WeightedSamples != 0
}

func actualInputCostMissingEvidenceBlocked(profile ActualInputCostProfile, nowMS int64) bool {
	if profile.MissingEvidenceStreak < actualInputCostMissingEvidenceThreshold || profile.LastMissingEvidenceMS <= 0 {
		return false
	}
	if nowMS < profile.LastMissingEvidenceMS {
		nowMS = profile.LastMissingEvidenceMS
	}
	return nowMS-profile.LastMissingEvidenceMS < actualInputCostMissingEvidenceCooldownMS
}
