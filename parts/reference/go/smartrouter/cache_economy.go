package smartrouter

import "math"

const (
	CacheEconomyProbabilityScale = int64(1_000_000)
	CacheEconomyFeatureVersion   = "cache-economy-features-v1"
	CacheEconomyChampionVersion  = "cache-economy-champion-v1"
	cacheEconomyMaxHorizon       = 16
)

type CacheEconomyAction string

const (
	CacheEconomyFallback CacheEconomyAction = "fallback_fixed"
	CacheEconomyKeep     CacheEconomyAction = "keep_affinity"
	CacheEconomySwitch   CacheEconomyAction = "switch_route"
)

type CacheEconomyReason string

const (
	CacheEconomyReasonDisabled        CacheEconomyReason = "fixed_mode"
	CacheEconomyReasonNoAffinity      CacheEconomyReason = "no_affinity"
	CacheEconomyReasonNoSessionKey    CacheEconomyReason = "no_session_key"
	CacheEconomyReasonLearning        CacheEconomyReason = "learning"
	CacheEconomyReasonDrift           CacheEconomyReason = "drift_detected"
	CacheEconomyReasonInvalidSnapshot CacheEconomyReason = "invalid_snapshot"
	CacheEconomyReasonAlreadyCheapest CacheEconomyReason = "already_best_cost"
	CacheEconomyReasonSharedCache     CacheEconomyReason = "shared_cache_cheaper"
	CacheEconomyReasonKeepCostsLess   CacheEconomyReason = "keep_costs_less"
	CacheEconomyReasonSwitchCostsLess CacheEconomyReason = "switch_costs_less"
	CacheEconomyReasonUncertain       CacheEconomyReason = "benefit_uncertain"
)

// CacheEconomySnapshot is a read-only, precomputed learning snapshot. The
// planner performs only bounded integer arithmetic over it; training and state
// mutation remain outside the request path.
type CacheEconomySnapshot struct {
	FeatureVersion             string           `json:"feature_version"`
	ModelVersion               string           `json:"model_version"`
	Samples                    int              `json:"samples"`
	MinimumSamples             int              `json:"minimum_samples"`
	ConfidencePPM              int64            `json:"confidence_ppm"`
	MinimumConfidencePPM       int64            `json:"minimum_confidence_ppm"`
	PredictionErrorPPM         int64            `json:"prediction_error_ppm"`
	WarmCostPerMillionContext  int64            `json:"warm_cost_per_million_context"`
	ColdCostPerMillionContext  int64            `json:"cold_cost_per_million_context"`
	ContextGrowthTokens        int              `json:"context_growth_tokens"`
	CompressionThresholdTokens int              `json:"compression_threshold_tokens"`
	ManualCompressionThreshold bool             `json:"manual_compression_threshold"`
	CacheSurvivalPPM           int64            `json:"cache_survival_ppm"`
	NamespaceSurvivalPPM       map[string]int64 `json:"namespace_survival_ppm,omitempty"`
	JointSurvivalPPM           []int64          `json:"joint_survival_ppm"`
	CurrentNamespaceRevision   string           `json:"current_namespace_revision"`
	Drifted                    bool             `json:"drifted"`
}

type CacheEconomyDecision struct {
	Action                CacheEconomyAction `json:"action"`
	Reason                CacheEconomyReason `json:"reason"`
	ModelVersion          string             `json:"model_version,omitempty"`
	Samples               int                `json:"samples,omitempty"`
	ConfidencePPM         int64              `json:"confidence_ppm,omitempty"`
	StayCostScore         int64              `json:"stay_cost_score,omitempty"`
	SwitchCostScore       int64              `json:"switch_cost_score,omitempty"`
	BenefitLowerBound     int64              `json:"benefit_lower_bound,omitempty"`
	BenefitProbabilityPPM int64              `json:"benefit_probability_ppm,omitempty"`
	PredictedUnitCost     int64              `json:"predicted_unit_cost,omitempty"`
	CurrentRouteID        string             `json:"current_route_id,omitempty"`
	TargetRouteID         string             `json:"target_route_id,omitempty"`
	CurrentRatioPPM       int64              `json:"current_ratio_ppm,omitempty"`
	TargetRatioPPM        int64              `json:"target_ratio_ppm,omitempty"`
	CounterfactualKnown   bool               `json:"counterfactual_known,omitempty"`
	UsedManualCompression bool               `json:"used_manual_compression,omitempty"`
}

func fallbackCacheEconomyDecision(reason CacheEconomyReason, snapshot *CacheEconomySnapshot) CacheEconomyDecision {
	decision := CacheEconomyDecision{Action: CacheEconomyFallback, Reason: reason}
	if snapshot != nil {
		decision.ModelVersion = snapshot.ModelVersion
		decision.Samples = snapshot.Samples
		decision.ConfidencePPM = snapshot.ConfidencePPM
	}
	return decision
}

// EvaluateCacheEconomy compares the current warm route with the strategy's
// best candidate over a finite horizon. All scores are relative and use the
// same fixed-point price snapshot, so no floating point or billing mutation is
// involved. Hard route eligibility has already been resolved by Filter.
func EvaluateCacheEconomy(
	snapshot *CacheEconomySnapshot,
	estimatedContext int,
	current Candidate,
	target Candidate,
) CacheEconomyDecision {
	if snapshot == nil {
		return fallbackCacheEconomyDecision(CacheEconomyReasonLearning, nil)
	}
	decision := fallbackCacheEconomyDecision(CacheEconomyReasonLearning, snapshot)
	decision.CurrentRouteID = current.Route.RouteID
	decision.TargetRouteID = target.Route.RouteID
	decision.CurrentRatioPPM = current.RatioPPM
	decision.TargetRatioPPM = target.RatioPPM
	decision.UsedManualCompression = snapshot.ManualCompressionThreshold
	if current.Route.RouteID == target.Route.RouteID && current.Route.ChannelID == target.Route.ChannelID {
		decision.Action = CacheEconomyKeep
		decision.Reason = CacheEconomyReasonAlreadyCheapest
		if snapshot.WarmCostPerMillionContext > 0 && current.RatioPPM >= 0 {
			decision.PredictedUnitCost = scaledCacheEconomyCost(snapshot.WarmCostPerMillionContext, current.RatioPPM)
		}
		return decision
	}
	if current.RatioPPM < 0 || target.RatioPPM < 0 || estimatedContext <= 0 {
		decision.Reason = CacheEconomyReasonInvalidSnapshot
		return decision
	}
	currentNamespace := current.Route.CacheNamespaceIdentity()
	targetNamespace := target.Route.CacheNamespaceIdentity()
	if currentNamespace != "" && currentNamespace == targetNamespace {
		decision.Action = CacheEconomySwitch
		decision.Reason = CacheEconomyReasonSharedCache
		decision.CounterfactualKnown = true
		if current.RatioPPM > target.RatioPPM {
			decision.BenefitProbabilityPPM = CacheEconomyProbabilityScale
		}
		if snapshot.WarmCostPerMillionContext > 0 {
			decision.PredictedUnitCost = scaledCacheEconomyCost(snapshot.WarmCostPerMillionContext, target.RatioPPM)
		}
		return decision
	}
	if snapshot.Drifted {
		decision.Reason = CacheEconomyReasonDrift
		return decision
	}
	minimumSamples := snapshot.MinimumSamples
	if minimumSamples <= 0 {
		minimumSamples = 8
	}
	minimumConfidence := snapshot.MinimumConfidencePPM
	if minimumConfidence <= 0 {
		minimumConfidence = 700_000
	}
	if snapshot.Samples < minimumSamples || snapshot.ConfidencePPM < minimumConfidence {
		return decision
	}
	if snapshot.WarmCostPerMillionContext <= 0 ||
		snapshot.ColdCostPerMillionContext < snapshot.WarmCostPerMillionContext ||
		snapshot.ContextGrowthTokens < 0 || snapshot.CompressionThresholdTokens <= estimatedContext {
		decision.Reason = CacheEconomyReasonInvalidSnapshot
		return decision
	}

	horizon := (snapshot.CompressionThresholdTokens-estimatedContext)/maxInt(snapshot.ContextGrowthTokens, 1) + 1
	if horizon < 1 {
		horizon = 1
	}
	if horizon > cacheEconomyMaxHorizon {
		horizon = cacheEconomyMaxHorizon
	}
	warmBase := int64(0)
	for index := 0; index < horizon; index++ {
		survival := CacheEconomyProbabilityScale
		if index < len(snapshot.JointSurvivalPPM) {
			survival = clampProbability(snapshot.JointSurvivalPPM[index])
		} else if len(snapshot.JointSurvivalPPM) > 0 {
			survival = clampProbability(snapshot.JointSurvivalPPM[len(snapshot.JointSurvivalPPM)-1])
		}
		contextTokens := int64(estimatedContext + index*snapshot.ContextGrowthTokens)
		callCost := saturatingMul(snapshot.WarmCostPerMillionContext, contextTokens)
		callCost = saturatingMul(callCost, survival) / CacheEconomyProbabilityScale
		warmBase = saturatingAdd(warmBase, callCost)
	}
	coldPremium := snapshot.ColdCostPerMillionContext - snapshot.WarmCostPerMillionContext
	// A generic cache-hit rate is not evidence that the specific target cache
	// namespace is still warm. Only a recent, session-local observation for the
	// exact namespace may reduce the conservative cold-start penalty.
	targetCacheSurvival := int64(0)
	if snapshot.NamespaceSurvivalPPM != nil {
		targetCacheSurvival = snapshot.NamespaceSurvivalPPM[targetNamespace]
	}
	coldProbability := CacheEconomyProbabilityScale - clampProbability(targetCacheSurvival)
	coldPenalty := saturatingMul(coldPremium, int64(estimatedContext))
	coldPenalty = saturatingMul(coldPenalty, coldProbability) / CacheEconomyProbabilityScale
	stayScore := saturatingMul(current.RatioPPM, warmBase)
	switchScore := saturatingMul(target.RatioPPM, saturatingAdd(warmBase, coldPenalty))
	stayUnitCost := scaledCacheEconomyCost(snapshot.WarmCostPerMillionContext, current.RatioPPM)
	switchUnitBase := saturatingAdd(
		snapshot.WarmCostPerMillionContext,
		saturatingMul(coldPremium, coldProbability)/CacheEconomyProbabilityScale,
	)
	switchUnitCost := scaledCacheEconomyCost(switchUnitBase, target.RatioPPM)
	decision.StayCostScore = stayScore
	decision.SwitchCostScore = switchScore
	decision.BenefitProbabilityPPM = cacheEconomyBenefitProbability(snapshot, stayScore, switchScore)
	if switchScore >= stayScore {
		decision.Action = CacheEconomyKeep
		decision.Reason = CacheEconomyReasonKeepCostsLess
		decision.BenefitLowerBound = stayScore - switchScore
		decision.PredictedUnitCost = stayUnitCost
		return decision
	}

	uncertaintyPPM := clampProbability(snapshot.PredictionErrorPPM)
	confidenceGap := CacheEconomyProbabilityScale - clampProbability(snapshot.ConfidencePPM)
	if confidenceGap > uncertaintyPPM {
		uncertaintyPPM = confidenceGap
	}
	uncertainty := saturatingMul(maxInt64(stayScore, switchScore), uncertaintyPPM) / CacheEconomyProbabilityScale
	minimumSaving := maxInt64(1, minInt64(stayScore, switchScore)/200) // 0.5%
	benefitLowerBound := stayScore - switchScore - uncertainty - minimumSaving
	decision.BenefitLowerBound = benefitLowerBound
	if benefitLowerBound <= 0 {
		decision.Action = CacheEconomyKeep
		decision.Reason = CacheEconomyReasonUncertain
		decision.PredictedUnitCost = stayUnitCost
		return decision
	}
	decision.Action = CacheEconomySwitch
	decision.Reason = CacheEconomyReasonSwitchCostsLess
	decision.PredictedUnitCost = switchUnitCost
	return decision
}

func cacheEconomyBenefitProbability(snapshot *CacheEconomySnapshot, stayScore, switchScore int64) int64 {
	if snapshot == nil || stayScore <= 0 || switchScore <= 0 {
		return 0
	}
	evidence := clampProbability(snapshot.ConfidencePPM)
	errorConfidence := CacheEconomyProbabilityScale - clampProbability(snapshot.PredictionErrorPPM)
	if errorConfidence < evidence {
		evidence = errorConfidence
	}
	if switchScore < stayScore {
		return CacheEconomyProbabilityScale/2 + evidence/2
	}
	return CacheEconomyProbabilityScale/2 - evidence/2
}

func scaledCacheEconomyCost(costPerMillion, ratioPPM int64) int64 {
	return saturatingMul(costPerMillion, ratioPPM) / CacheEconomyProbabilityScale
}

func clampProbability(value int64) int64 {
	if value < 0 {
		return 0
	}
	if value > CacheEconomyProbabilityScale {
		return CacheEconomyProbabilityScale
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func saturatingMul(left, right int64) int64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left > math.MaxInt64/right {
		return math.MaxInt64
	}
	return left * right
}
