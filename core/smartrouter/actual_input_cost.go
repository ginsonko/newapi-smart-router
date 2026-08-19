package smartrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
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
	ActualInputCostEstimatorVersion          = "actual-input-cost-ranking-v2"
)

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
}

// ActualInputCostSnapshot is immutable after publication by the service
// learner.  Callers must treat the map and its values as read-only.
type ActualInputCostSnapshot map[string]ActualInputCostProfile

type ActualInputCostObservation struct {
	Key                   string
	PoolKey               string
	EventMS               int64
	RegularInputTokens    int64
	CacheReadTokens       int64
	CacheCreation5mTokens int64
	CacheCreation1hTokens int64
	TotalInputTokens      int64
	Reliable              bool
	MissingEvidence       bool
}

type ActualInputCostEstimate struct {
	EffectivePPM             int64  `json:"effective_ppm"`
	CacheReadRatePPM         int64  `json:"cache_read_rate_ppm,omitempty"`
	ObservedCacheReadRatePPM *int64 `json:"observed_cache_read_rate_ppm,omitempty"`
	Source                   string `json:"source"`
	Samples                  int64  `json:"samples,omitempty"`
	LastObservedAtMS         int64  `json:"last_observed_at_ms,omitempty"`
	EvidenceAgeMS            int64  `json:"evidence_age_ms,omitempty"`
	HasRealEvidence          bool   `json:"has_real_evidence"`
	Optimistic               bool   `json:"optimistic"`
	Comparable               bool   `json:"comparable"`
}

const (
	ActualInputCostObserved       = "observed"
	ActualInputCostPooledPrior    = "pooled_prior"
	ActualInputCostOptimistic     = "optimistic_unobserved"
	ActualInputCostUnobservable   = "unobservable_static"
	ActualInputCostStaticFallback = "static_fallback"
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
	decay := 1.0
	if previous.LastEventMS > 0 {
		delta := float64(nowMS - previous.LastEventMS)
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
	previous.LastEventMS = nowMS
	previous.MissingEvidenceStreak = 0
	previous.LastMissingEvidenceMS = 0
	previous.Version = ActualInputCostFeatureVersion
	return previous
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

// MergeActualInputCostProfiles combines independent exact profiles without
// changing their weighting. Invalid profiles are ignored, so observability can
// disappear safely without affecting route ordering or request handling.
func MergeActualInputCostProfiles(profiles ...ActualInputCostProfile) (ActualInputCostProfile, bool) {
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
// one stable planning time. A nil snapshot means the learner is not ready and
// fails back to static ordering. A ready, non-nil snapshot without exact
// evidence uses the hidden 90% cache-read cold-start prior.
func EstimateActualInputCostAt(
	price RoutePrice,
	staticScorePPM int64,
	key string,
	poolKey string,
	snapshot ActualInputCostSnapshot,
	nowMS int64,
) ActualInputCostEstimate {
	fallback := ActualInputCostEstimate{
		EffectivePPM: staticScorePPM,
		Source:       ActualInputCostStaticFallback,
		Comparable:   price.StaticComparable && price.ActualInputComparisonClass != "",
	}
	if !fallback.Comparable || staticScorePPM < 0 || !price.CachePricingKnown || snapshot == nil || key == "" {
		return fallback
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
	if actualInputCostMissingEvidenceBlocked(exact, nowMS) {
		fallback.Source = ActualInputCostUnobservable
		return fallback
	}

	priorRead := float64(actualInputCostOptimisticCacheReadPPM) / float64(actualInputCostPPMScale)
	regular, read, create5m, create1h := 1-priorRead, priorRead, 0.0, 0.0
	forceOptimisticRetry := exact.MissingEvidenceStreak > 0
	agedRegular, agedRead, agedCreate5m, agedCreate1h, observedReadRate, _, ageMS, exactValid := actualInputCostAgedMixAt(exact, nowMS)
	if !forceOptimisticRetry && exactValid {
		regular, read = agedRegular, agedRead
		create5m, create1h = agedCreate5m, agedCreate1h
		fallback.Source = ActualInputCostObserved
		fallback.Samples = exact.TotalSamples
		fallback.LastObservedAtMS = exact.LastEventMS
		fallback.EvidenceAgeMS = ageMS
		fallback.HasRealEvidence = true
		fallback.ObservedCacheReadRatePPM = &observedReadRate
	} else {
		if actualInputCostProfileHasTokenState(exact) && !exactValid {
			return fallback
		}
		fallback.Source = ActualInputCostOptimistic
		fallback.Optimistic = true
	}

	total := regular + read + create5m + create1h
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		return fallback
	}
	cacheReadRatio := float64(price.CacheReadRatioPPM) / float64(actualInputCostPPMScale)
	create5mRatio := float64(price.CacheCreation5mRatioPPM) / float64(actualInputCostPPMScale)
	create1hRatio := float64(price.CacheCreation1hRatioPPM) / float64(actualInputCostPPMScale)
	coefficient := (regular + read*cacheReadRatio + create5m*create5mRatio + create1h*create1hRatio) / total
	if coefficient < 0 || math.IsNaN(coefficient) || math.IsInf(coefficient, 0) {
		return fallback
	}
	value := float64(staticScorePPM) * coefficient
	if math.IsNaN(value) || math.IsInf(value, 0) || value >= float64(math.MaxInt64) {
		return fallback
	}
	if value < 0 {
		return fallback
	}
	fallback.EffectivePPM = int64(math.Round(value))
	cacheReadRate := math.Round(read / total * float64(actualInputCostPPMScale))
	if cacheReadRate < 0 || cacheReadRate > float64(actualInputCostPPMScale) || math.IsNaN(cacheReadRate) || math.IsInf(cacheReadRate, 0) {
		return fallback
	}
	fallback.CacheReadRatePPM = int64(cacheReadRate)
	fallback.Comparable = true
	return fallback
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
	trust := math.Pow(2, -float64(ageMS)/actualInputCostEvidenceHalfLifeMS)
	if trust < 0 || trust > 1 || math.IsNaN(trust) || math.IsInf(trust, 0) {
		return 0, 0, 0, 0, 0, 0, 0, false
	}
	priorRead := float64(actualInputCostOptimisticCacheReadPPM) / float64(actualInputCostPPMScale)
	regular = regular*trust + (1-priorRead)*(1-trust)
	read = read*trust + priorRead*(1-trust)
	create5m *= trust
	create1h *= trust
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
