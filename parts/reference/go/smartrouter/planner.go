package smartrouter

import (
	"errors"
	"fmt"
	"sort"
)

// Unknown routes need a small, deterministic exploration lane when a strategy
// would otherwise always prefer routes with known TTFT/stability. The actual
// upstream attempt is still fenced by the shared probe lease and budget in the
// service state store; this value only decides which matching requests may
// carry a probe candidate to the front of an otherwise valid plan.
const unknownRouteProbeTrafficPPM int64 = 100_000

var (
	ErrPolicyDisabled    = errors.New("smartrouter: policy is disabled")
	ErrUnsupportedReplay = errors.New("smartrouter: request is not safe to replay")
	ErrAlreadyCommitted  = errors.New("smartrouter: response is already committed")
	ErrAttemptBudget     = errors.New("smartrouter: attempt budget is exhausted")
	ErrContractNotFound  = errors.New("smartrouter: automatic route contract not found")
	ErrContractMismatch  = errors.New("smartrouter: request does not match the automatic route contract")
	ErrPoolNotFound      = errors.New("smartrouter: automatic route pool not found")
	ErrPriceUnavailable  = errors.New("smartrouter: price snapshot is unavailable")
	ErrNoCandidate       = errors.New("smartrouter: no eligible automatic route")
)

type RouteRequest struct {
	ContractID            string      `json:"contract_id"`
	CanonicalModel        string      `json:"canonical_model"`
	Endpoint              string      `json:"endpoint"`
	CapabilityFingerprint string      `json:"capability_fingerprint"`
	RequiredCapabilities  uint64      `json:"required_capabilities"`
	EstimatedContext      int         `json:"estimated_context"`
	ReplayClass           ReplayClass `json:"replay_class"`
	WarmingKey            string      `json:"warming_key,omitempty"`
	// AdmissionKey is an internal, stable request/session identifier used for
	// deterministic recovery sampling. It is deliberately separate from
	// WarmingKey: the latter is a caller's prompt-cache identity and is the
	// only input eligible to create strong cache affinity.
	AdmissionKey string `json:"-"`
	// ClientClass is an irreversible coarse fingerprint used only for partial
	// pooling of cache-economy statistics. Raw User-Agent values are never kept.
	ClientClass string `json:"-"`
	DeadlineMS  int64  `json:"deadline_ms,omitempty"`
}

type AttemptState struct {
	MaxAttempts             int      `json:"max_attempts"`
	StartedAttempts         int      `json:"started_attempts"`
	AttemptedRoutes         []string `json:"attempted_routes"`
	AttemptedChannels       []int    `json:"attempted_channels"`
	Committed               bool     `json:"committed"`
	RecoveryProbeUsed       bool     `json:"recovery_probe_used"`
	ExhaustEligibleChannels bool     `json:"exhaust_eligible_channels"`
	// VirginMediaRetryAuthorized is a one-shot controller grant. It is set only
	// after an image/video attempt is known not to have reached submission, and
	// consumed when the next concrete attempt starts. It must never be inferred
	// from a transport failure or another acceptance-ambiguous result.
	VirginMediaRetryAuthorized bool `json:"virgin_media_retry_authorized"`
	Requeued                   bool `json:"requeued"`
}

type AffinityStrength string

const (
	AffinityStrong AffinityStrength = "strong"
	AffinityWeak   AffinityStrength = "weak"
)

type AffinityState struct {
	Strength               AffinityStrength `json:"strength"`
	ContractID             string           `json:"contract_id"`
	RouteID                string           `json:"route_id"`
	ChannelID              int              `json:"channel_id"`
	CacheDomain            string           `json:"cache_domain"`
	CacheNamespaceRevision string           `json:"cache_namespace_revision,omitempty"`
	PolicyID               string           `json:"policy_id"`
	LastSuccessMS          int64            `json:"last_success_ms"`
	LastSwitchMS           int64            `json:"last_switch_ms"`
	LastActivityMS         int64            `json:"last_activity_ms"`
}

type TTFTSnapshot struct {
	Version string               `json:"version"`
	Routes  map[string]TTFTState `json:"routes"`
}

type TTFTState struct {
	Samples      int   `json:"samples"`
	P95MS        int64 `json:"p95_ms"`
	FreshUntilMS int64 `json:"fresh_until_ms"`
}

type PlanInput struct {
	NowMS                    int64
	RecoveryEvidenceTTLMS    int64
	Request                  RouteRequest
	Policy                   Policy
	Catalog                  CertifiedRouteCatalog
	Prices                   PriceSnapshot
	Quality                  QualitySnapshot
	TTFT                     TTFTSnapshot
	Capacity                 CapacitySnapshot
	Credentials              CredentialSnapshot
	AllowedGroups            []string
	Affinity                 *AffinityState
	CacheEconomy             *CacheEconomySnapshot
	WeakAffinityTolerancePPM int64
	Attempt                  AttemptState
	// BackgroundRecovery keeps unknown and known-bad recovery off the request
	// path. The worker establishes the first recovery success; deterministic
	// real-traffic canaries then promote warming routes back to healthy.
	BackgroundRecovery bool
}

type AdmissionKind string

const (
	AdmissionNormal          AdmissionKind = "normal"
	AdmissionBootstrap       AdmissionKind = "bootstrap"
	AdmissionVirginBootstrap AdmissionKind = "virgin_bootstrap"
	AdmissionProbe           AdmissionKind = "probe"
	AdmissionWarming         AdmissionKind = "warming"
	AdmissionWarmSample      AdmissionKind = "warming_sample"
	AdmissionDegraded        AdmissionKind = "degraded"
	AdmissionLastResort      AdmissionKind = "last_resort"
)

type TTFTKnowledge string

const (
	TTFTDisabled TTFTKnowledge = "disabled"
	TTFTUnknown  TTFTKnowledge = "unknown"
	TTFTKnown    TTFTKnowledge = "known"
)

type Candidate struct {
	Route            CertifiedRoute
	RatioPPM         int64
	Price            RoutePrice
	PoolIndex        int
	ManualIndex      int
	Quality          QualityState
	Credential       CredentialState
	CredentialDomain string
	Admission        AdmissionKind
	KeySuppressed    bool
	StabilityPPM     int64
	BalancedScorePPM int64
	TTFTKnowledge    TTFTKnowledge
	TTFTP95MS        int64
	TTFTSlow         bool
	TTFTAboveTarget  bool
	priceClassRank   int
}

type RejectReason string

const (
	RejectGroupUnauthorized   RejectReason = "group_unauthorized"
	RejectGroupNotSelected    RejectReason = "group_not_selected"
	RejectGroupExcluded       RejectReason = "group_excluded"
	RejectRouteExcluded       RejectReason = "route_excluded"
	RejectAlreadyAttempted    RejectReason = "already_attempted"
	RejectChannelAttempted    RejectReason = "channel_already_attempted"
	RejectPriceMissing        RejectReason = "price_missing"
	RejectAbovePriceLimit     RejectReason = "above_price_limit"
	RejectCapabilityMismatch  RejectReason = "capability_mismatch"
	RejectContextTooLarge     RejectReason = "context_too_large"
	RejectHealthUnavailable   RejectReason = "health_unavailable"
	RejectRecoveryBudget      RejectReason = "recovery_budget_exhausted"
	RejectRecoveryCooling     RejectReason = "recovery_cooling"
	RejectKeySuppressed       RejectReason = "key_suppressed"
	RejectQuarantined         RejectReason = "quarantined"
	RejectCredentialBlocked   RejectReason = "credential_blocked"
	RejectUnlistedManualRoute RejectReason = "unlisted_manual_route"
	RejectTTFTSlow            RejectReason = "ttft_slow"
)

type Rejection struct {
	RouteID string       `json:"route_id"`
	Reason  RejectReason `json:"reason"`
}

type FilterInput struct {
	NowMS                 int64
	RecoveryEvidenceTTLMS int64
	Request               RouteRequest
	Policy                Policy
	Catalog               CertifiedRouteCatalog
	Prices                PriceSnapshot
	Quality               QualitySnapshot
	TTFT                  TTFTSnapshot
	Credentials           CredentialSnapshot
	AllowedGroups         []string
	Affinity              *AffinityState
	Attempt               AttemptState
	BackgroundRecovery    bool
}

type FilterResult struct {
	ContractID   string
	PoolID       string
	OrderMode    OrderMode
	Strategy     Strategy
	Candidates   []Candidate
	Rejections   []Rejection
	TTFTFallback bool
}

type PlanKind string

const (
	PlanAttempt PlanKind = "attempt"
	PlanQueue   PlanKind = "queue"
)

type AffinityDisposition string

const (
	AffinityNone             AffinityDisposition = "none"
	AffinityPreserve         AffinityDisposition = "preserve"
	AffinityReplaceOnSuccess AffinityDisposition = "replace_on_success"
)

type PlanResult struct {
	Kind                PlanKind
	ContractID          string
	PoolID              string
	CatalogVersion      string
	PriceVersion        string
	RouteID             string
	ChannelID           int
	Group               string
	UpstreamModel       string
	RatioPPM            int64
	Price               RoutePrice
	CacheDomain         string
	CapacityDomain      string
	Admission           AdmissionKind
	RouteEpoch          uint64
	MaxInflightHint     int
	MaxQueueWaitMS      int64
	AffinityDisposition AffinityDisposition
	CacheEconomy        CacheEconomyDecision
	CapacityOverflow    bool
	Candidates          []Candidate
	Rejections          []Rejection
}

func Filter(input FilterInput) (FilterResult, error) {
	input.Policy = NormalizePolicy(input.Policy)
	if err := input.Policy.Validate(); err != nil {
		return FilterResult{}, err
	}
	if !input.Policy.Enabled {
		return FilterResult{}, ErrPolicyDisabled
	}
	if !IsRoutableReplayClass(input.Request.ReplayClass) {
		return FilterResult{}, fmt.Errorf("%w: %s", ErrUnsupportedReplay, input.Request.ReplayClass.String())
	}
	if input.Attempt.Committed {
		return FilterResult{}, ErrAlreadyCommitted
	}
	if input.Attempt.MaxAttempts <= 0 || input.Attempt.StartedAttempts < 0 ||
		(input.Attempt.StartedAttempts >= input.Attempt.MaxAttempts &&
			!(input.Attempt.ExhaustEligibleChannels && input.Request.ReplayClass == ReplaySafeText)) {
		return FilterResult{}, ErrAttemptBudget
	}
	if input.Prices.Version == "" || input.Prices.RatiosPPM == nil {
		return FilterResult{}, ErrPriceUnavailable
	}
	contractEntry, ok := input.Catalog.Contracts[input.Request.ContractID]
	if !ok {
		return FilterResult{}, ErrContractNotFound
	}
	if !requestMatchesContract(input.Request, contractEntry.Contract) {
		return FilterResult{}, ErrContractMismatch
	}
	poolID, orderMode, manualOrder := resolveContractPolicy(input.Policy, input.Request.ContractID)
	if input.Policy.Version == PolicyVersion {
		if _, exists := contractEntry.Pools[AutomaticPoolID]; exists {
			poolID = AutomaticPoolID
		} else if input.Policy.PoolPolicy != "" {
			// Transitional compatibility for a v4 policy exercised against a
			// v3 catalog snapshot during rolling upgrade.
			poolID = input.Policy.PoolPolicy
		}
	}
	strategy := input.Policy.EffectiveStrategy()
	if orderMode == OrderManual {
		strategy = StrategyManual
	}
	metricPolicy := effectiveTTFTMetricPolicy(input.Policy.TTFTPolicy, strategy)
	pool, ok := contractEntry.Pools[poolID]
	if !ok {
		return FilterResult{}, fmt.Errorf("%w: %s", ErrPoolNotFound, poolID)
	}

	allowedGroups := stringSet(input.AllowedGroups)
	excludedGroups := stringSet(input.Policy.ExcludedGroups)
	excludedRoutes := stringSet(input.Policy.ExcludedRoutes)
	attemptedRoutes := stringSet(input.Attempt.AttemptedRoutes)
	attemptedChannels := intSet(input.Attempt.AttemptedChannels)
	manualRanks := routeRanks(manualOrder)
	manualGroupRanks := routeRanks(input.Policy.ManualGroupOrder)
	priceClassRanks := make(map[string]int)
	result := FilterResult{
		ContractID: input.Request.ContractID,
		PoolID:     poolID,
		OrderMode:  orderMode,
		Strategy:   strategy,
		Candidates: make([]Candidate, 0, len(pool.Candidates)),
		Rejections: make([]Rejection, 0),
	}
	virginBootstrap := make([]Candidate, 0)

	for index, route := range pool.Candidates {
		reason := RejectReason("")
		switch {
		case !allowedGroups[route.Group]:
			reason = RejectGroupUnauthorized
		case !input.Policy.AllowsGroup(route.Group):
			reason = RejectGroupNotSelected
		case excludedGroups[route.Group]:
			reason = RejectGroupExcluded
		case excludedRoutes[route.RouteID]:
			reason = RejectRouteExcluded
		case attemptedRoutes[route.RouteID]:
			reason = RejectAlreadyAttempted
		case attemptedChannels[route.ChannelID]:
			reason = RejectChannelAttempted
		case orderMode == OrderManual && !input.Policy.AllowFutureCertifiedRoutes:
			if len(manualRanks) > 0 && !hasRouteRank(manualRanks, route.RouteID) {
				reason = RejectUnlistedManualRoute
			} else if len(manualRanks) == 0 && len(manualGroupRanks) > 0 && !hasRouteRank(manualGroupRanks, route.Group) {
				reason = RejectUnlistedManualRoute
			}
		case route.Capabilities&input.Request.RequiredCapabilities != input.Request.RequiredCapabilities:
			reason = RejectCapabilityMismatch
		case input.Request.EstimatedContext < 0 || input.Request.EstimatedContext > route.MaxContext:
			reason = RejectContextTooLarge
		}
		ratio, hasPrice := input.Prices.RatiosPPM[route.RouteID]
		if reason == "" && (!hasPrice || ratio < 0) {
			reason = RejectPriceMissing
		}
		if reason == "" && ratio > input.Policy.MaxEffectiveRatioPPM {
			reason = RejectAbovePriceLimit
		}
		if reason != "" {
			result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: reason})
			continue
		}

		quality, found := input.Quality.Routes[route.RouteID]
		if !found {
			quality = InitialQuality(route.StableFallback)
		}
		credentialDomain := RouteCredentialDomain(route)
		credential := input.Credentials.Domains[credentialDomain]
		if credential.BlockedUntilMS > input.NowMS {
			result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: RejectCredentialBlocked})
			continue
		}
		admission, suppressed, healthRejection := qualityAdmission(route, quality, input)
		virginUnknown := input.BackgroundRecovery && healthRejection == RejectHealthUnavailable &&
			IsVirginUnknown(quality)
		bootstrapUnknown := virginUnknown && !input.Attempt.RecoveryProbeUsed
		if bootstrapUnknown {
			admission = AdmissionVirginBootstrap
			suppressed = false
			healthRejection = ""
		} else if virginUnknown && input.Attempt.RecoveryProbeUsed {
			healthRejection = RejectRecoveryBudget
		}
		if healthRejection == "" && admission == AdmissionProbe && IsVirginUnknown(quality) {
			admission = AdmissionVirginBootstrap
		}
		mediaVirginRetry := admission == AdmissionVirginBootstrap &&
			input.Attempt.VirginMediaRetryAuthorized && isReplaySafeMedia(input.Request.ReplayClass)
		if healthRejection == "" && isRecoveryAttemptAdmission(admission) && input.Attempt.RecoveryProbeUsed && !mediaVirginRetry {
			healthRejection = RejectRecoveryBudget
		}
		if healthRejection != "" {
			result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: healthRejection})
			continue
		}
		manualIndex := manualCandidateIndex(route, index, len(pool.Candidates), manualRanks, manualGroupRanks)
		routePrice := routePriceForCandidate(input.Prices, route.RouteID, ratio)
		priceClass := routePrice.ComparisonClass
		if !routePrice.StaticComparable {
			priceClass = "incomparable:" + route.RouteID
		}
		priceClassRank, exists := priceClassRanks[priceClass]
		if !exists {
			priceClassRank = len(priceClassRanks)
			priceClassRanks[priceClass] = priceClassRank
		}
		candidate := Candidate{
			Route:            route,
			RatioPPM:         ratio,
			Price:            routePrice,
			PoolIndex:        index,
			ManualIndex:      manualIndex,
			Quality:          quality,
			Credential:       credential,
			CredentialDomain: credentialDomain,
			Admission:        admission,
			KeySuppressed:    suppressed,
			StabilityPPM:     qualityStabilityPPM(quality),
			priceClassRank:   priceClassRank,
		}
		applyTTFTKnowledge(&candidate, input.TTFT.Routes[route.RouteID], metricPolicy, input.NowMS)
		if bootstrapUnknown {
			virginBootstrap = append(virginBootstrap, candidate)
		} else {
			result.Candidates = append(result.Candidates, candidate)
		}
	}
	if strategy == StrategyPrice && len(virginBootstrap) > 0 {
		result.Candidates = append(result.Candidates, virginBootstrap...)
	} else if len(result.Candidates) == 0 && len(virginBootstrap) > 0 {
		result.Candidates = append(result.Candidates, virginBootstrap...)
	} else {
		for _, candidate := range virginBootstrap {
			result.Rejections = append(result.Rejections, Rejection{
				RouteID: candidate.Route.RouteID, Reason: RejectHealthUnavailable,
			})
		}
	}

	result.Candidates, result.TTFTFallback = applyTTFTPolicy(result.Candidates, input.Policy.TTFTPolicy, &result.Rejections)
	assignBalancedScores(result.Candidates, input.Policy.EffectiveBalancedWeights())
	sortCandidates(result.Candidates, strategy, result.TTFTFallback, input.BackgroundRecovery)
	result.Candidates = promoteUnknownProbeCandidate(result.Candidates, input.Request, strategy)
	result.Candidates = limitRecoveryProbeCandidates(
		result.Candidates,
		&result.Rejections,
		input.BackgroundRecovery && strategy == StrategyPrice,
	)
	if len(result.Candidates) == 0 {
		return result, ErrNoCandidate
	}
	return result, nil
}

func limitRecoveryProbeCandidates(candidates []Candidate, rejections *[]Rejection, preserveVirgin bool) []Candidate {
	kept := candidates[:0]
	probeSelected := false
	for _, candidate := range candidates {
		if !isRecoveryAttemptAdmission(candidate.Admission) {
			kept = append(kept, candidate)
			continue
		}
		if candidate.Admission == AdmissionVirginBootstrap && preserveVirgin {
			kept = append(kept, candidate)
			continue
		}
		if !probeSelected {
			probeSelected = true
			kept = append(kept, candidate)
			continue
		}
		*rejections = append(*rejections, Rejection{
			RouteID: candidate.Route.RouteID, Reason: RejectRecoveryBudget,
		})
	}
	return kept
}

func isRecoveryAttemptAdmission(admission AdmissionKind) bool {
	return admission == AdmissionProbe || admission == AdmissionVirginBootstrap
}

func isReplaySafeMedia(class ReplayClass) bool {
	return class == ReplaySafeImage || class == ReplaySafeVideo || class == ReplaySafeAudio
}

func promoteUnknownProbeCandidate(candidates []Candidate, request RouteRequest, strategy Strategy) []Candidate {
	// Price and manual/stability strategies must never silently replace their
	// explicit preference with an exploratory route. Latency and balanced modes
	// opt into a small discovery lane because otherwise an unknown route can
	// remain unmeasured forever behind known candidates.
	if strategy != StrategyLatency && strategy != StrategyBalanced {
		return candidates
	}
	if len(candidates) < 2 {
		return candidates
	}
	key := request.AdmissionKey
	if key == "" {
		key = request.WarmingKey
	}
	if key == "" {
		return candidates
	}
	for index, candidate := range candidates {
		if index == 0 || !isRecoveryAttemptAdmission(candidate.Admission) || candidate.Quality.Phase != QualityUnknown {
			continue
		}
		epoch := candidate.Quality.Epoch
		if epoch == 0 {
			epoch = 1
		}
		if !DeterministicAdmission(key, candidate.Route.RouteID, epoch, unknownRouteProbeTrafficPPM) {
			continue
		}
		selected := candidate
		copy(candidates[1:index+1], candidates[:index])
		candidates[0] = selected
		return candidates
	}
	return candidates
}

func effectiveTTFTMetricPolicy(policy TTFTPolicy, strategy Strategy) TTFTPolicy {
	if policy.Enabled || (strategy != StrategyLatency && strategy != StrategyBalanced) {
		return policy
	}
	policy.Enabled = true
	policy.Metric = TTFTMetricP95
	policy.TargetMS = int64(^uint64(0) >> 2)
	policy.HardMaxMS = int64(^uint64(0) >> 1)
	if strategy == StrategyLatency {
		policy.MinSamples = 20
	} else {
		policy.MinSamples = 1
	}
	return policy
}

func manualCandidateIndex(
	route CertifiedRoute,
	poolIndex int,
	poolSize int,
	routeRanks map[string]int,
	groupRanks map[string]int,
) int {
	if routeRank, ranked := routeRanks[route.RouteID]; ranked {
		return routeRank
	}
	base := len(routeRanks)
	stride := poolSize + 1
	if groupRank, ranked := groupRanks[route.Group]; ranked {
		return base + groupRank*stride + poolIndex
	}
	return base + (len(groupRanks)+1)*stride + poolIndex
}

func Plan(input PlanInput) (PlanResult, error) {
	filtered, err := Filter(FilterInput{
		NowMS:                 input.NowMS,
		RecoveryEvidenceTTLMS: input.RecoveryEvidenceTTLMS,
		Request:               input.Request,
		Policy:                input.Policy,
		Catalog:               input.Catalog,
		Prices:                input.Prices,
		Quality:               input.Quality,
		TTFT:                  input.TTFT,
		Credentials:           input.Credentials,
		AllowedGroups:         input.AllowedGroups,
		Affinity:              input.Affinity,
		Attempt:               input.Attempt,
		BackgroundRecovery:    input.BackgroundRecovery,
	})
	if err != nil {
		return PlanResult{Candidates: filtered.Candidates, Rejections: filtered.Rejections}, err
	}

	ordered, affinityEligible, economyDecision := applyAffinity(
		filtered.Candidates,
		input.Affinity,
		input.CacheEconomy,
		input.Request.EstimatedContext,
		filtered.ContractID,
		input.WeakAffinityTolerancePPM,
		input.Policy.AffinityMaxPremiumPercent,
		input.Policy.EffectiveAffinityMode(),
		input.Policy.EffectiveStrategy(),
		input.Policy.HealthGuard,
	)
	choice, err := chooseCapacity(ordered, input.Capacity, input.Policy.QueuePolicy, input.NowMS, input.Request.DeadlineMS, input.Attempt.Requeued)
	if err != nil {
		return PlanResult{Candidates: ordered, Rejections: filtered.Rejections}, err
	}
	disposition := affinityDisposition(input.Affinity, choice.Candidate, affinityEligible)
	overflow := input.Affinity != nil && input.Affinity.Strength == AffinityStrong && affinityEligible && choice.Candidate.Route.RouteID != input.Affinity.RouteID
	return PlanResult{
		Kind:                choice.Kind,
		ContractID:          filtered.ContractID,
		PoolID:              filtered.PoolID,
		CatalogVersion:      input.Catalog.Version,
		PriceVersion:        input.Prices.Version,
		RouteID:             choice.Candidate.Route.RouteID,
		ChannelID:           choice.Candidate.Route.ChannelID,
		Group:               choice.Candidate.Route.Group,
		UpstreamModel:       choice.Candidate.Route.UpstreamModel,
		RatioPPM:            choice.Candidate.RatioPPM,
		Price:               choice.Candidate.Price,
		CacheDomain:         choice.Candidate.Route.CacheDomain,
		CapacityDomain:      choice.Candidate.Route.CapacityDomain,
		Admission:           choice.Candidate.Admission,
		RouteEpoch:          choice.Candidate.Quality.Epoch,
		MaxInflightHint:     admissionCapacityLimit(choice.Candidate.Admission),
		MaxQueueWaitMS:      choice.MaxQueueWaitMS,
		AffinityDisposition: disposition,
		CacheEconomy:        economyDecision,
		CapacityOverflow:    overflow,
		Candidates:          ordered,
		Rejections:          filtered.Rejections,
	}, nil
}

// ExplainPlan is the side-effect-free decision entry point for status and
// diagnostics. It intentionally delegates to Plan so explanation ordering,
// rejection codes, affinity handling, TTFT filtering, and capacity spill or
// queue behavior cannot drift into a second routing algorithm.
//
// Callers must label the result as a current-policy preference snapshot. It is
// not a receipt for any prior request and it never acquires a lease or mutates
// routing state.
func ExplainPlan(input PlanInput) (PlanResult, error) {
	return Plan(input)
}

func requestMatchesContract(request RouteRequest, contract RouteContract) bool {
	return request.ContractID != "" && request.ContractID == contract.ContractID &&
		request.CanonicalModel != "" && request.CanonicalModel == contract.CanonicalModel &&
		request.Endpoint != "" && request.Endpoint == contract.Endpoint &&
		request.CapabilityFingerprint != "" && request.CapabilityFingerprint == contract.CapabilityFingerprint
}

func resolveContractPolicy(policy Policy, contractID string) (string, OrderMode, []string) {
	poolID := policy.PoolPolicy
	if policy.Version == PolicyVersion {
		poolID = AutomaticPoolID
	}
	orderMode := orderModeFromStrategy(policy.EffectiveStrategy())
	var manualOrder []string
	if override, ok := policy.ContractOverrides[contractID]; ok {
		if policy.Version == LegacyPolicyVersion && override.PoolPolicy != "" {
			poolID = override.PoolPolicy
		}
		if override.OrderMode != "" {
			orderMode = override.OrderMode
		}
		manualOrder = override.ManualRouteOrder
	}
	return poolID, orderMode, manualOrder
}

func qualityAdmission(route CertifiedRoute, quality QualityState, input FilterInput) (AdmissionKind, bool, RejectReason) {
	if !input.Policy.HealthGuard {
		return AdmissionNormal, false, ""
	}
	if quality.Phase == QualityQuarantined {
		return "", true, RejectQuarantined
	}
	if input.BackgroundRecovery {
		if quality.Phase == QualityHalfOpen {
			// A background worker already owns the fenced recovery attempt. Do not
			// make foreground traffic race that probe for capacity or evidence.
			return "", false, RejectHealthUnavailable
		}
		switch quality.Phase {
		case QualityHealthy:
			if recoveredRouteCooling(quality, input) {
				return "", false, RejectRecoveryCooling
			}
			return AdmissionNormal, false, ""
		case QualityBootstrap:
			// Bootstrap has no failure evidence. Preserve the one-request cold-start
			// lane used by legacy stable routes; unknown and known-bad routes are
			// recovered by the background worker instead.
			return AdmissionBootstrap, false, ""
		case QualityDegraded:
			// A key may explicitly tolerate a small number of failures. Keep that
			// cheap route usable until its threshold is reached, independent of the
			// ordering strategy.
			if KeySuppressed(quality, input.Policy.EffectiveFailureThreshold(), input.NowMS) {
				return "", true, RejectKeySuppressed
			}
			return AdmissionDegraded, false, ""
		case QualityWarming:
			if hasFreshSuccessfulEvidence(quality, input.NowMS, input.RecoveryEvidenceTTLMS) {
				return AdmissionWarming, false, ""
			}
			if recoveredRouteCooling(quality, input) {
				return "", false, RejectRecoveryCooling
			}
			admissionKey := input.Request.AdmissionKey
			if admissionKey == "" {
				admissionKey = input.Request.WarmingKey
			}
			if DeterministicAdmission(admissionKey, route.RouteID, quality.WarmingEpoch, quality.WarmingTrafficPPM) {
				return AdmissionWarming, false, ""
			}
			return "", false, RejectHealthUnavailable
		case QualityOpen:
			// Open is shared route evidence, while failure_threshold is a
			// deliberate per-Key tolerance. A tolerant Key may take the next
			// degraded attempt until its own threshold is reached; the default
			// Key is suppressed after the configured consecutive failures.
			if KeySuppressed(quality, input.Policy.EffectiveFailureThreshold(), input.NowMS) {
				return "", true, RejectKeySuppressed
			}
			return AdmissionDegraded, false, ""
		default:
			// With a live background recovery plane, ordinary requests must never
			// linearly probe open, half-open, unknown, or otherwise known-bad
			// routes. If no serving candidate remains, Plan fails immediately.
			return "", KeySuppressed(quality, input.Policy.EffectiveFailureThreshold(), input.NowMS), RejectHealthUnavailable
		}
	}
	suppressed := KeySuppressed(quality, input.Policy.EffectiveFailureThreshold(), input.NowMS)
	if suppressed {
		// A first failure from a healthy route enters Degraded and schedules a
		// warm sample instead of setting NextProbeAtMS. Honour that sample
		// deadline when this key's lower threshold suppresses the route; without
		// it, threshold=1 would immediately dispatch the same known-bad route
		// again on the next request.
		nextProbeAtMS := quality.NextProbeAtMS
		if quality.Phase == QualityDegraded || quality.Phase == QualityWarming {
			nextProbeAtMS = max64(nextProbeAtMS, quality.SampleDueAtMS)
		}
		if nextProbeAtMS <= input.NowMS {
			return AdmissionProbe, true, ""
		}
		return "", true, RejectKeySuppressed
	}
	switch quality.Phase {
	case QualityHealthy:
		if recoveredRouteCooling(quality, input) && !strongAffinityMatches(input.Affinity, input.Request.ContractID, route) {
			return "", false, RejectRecoveryCooling
		}
		return AdmissionNormal, false, ""
	case QualityBootstrap:
		if route.StableFallback {
			return AdmissionBootstrap, false, ""
		}
		return AdmissionProbe, false, ""
	case QualityDegraded:
		if quality.SampleDueAtMS <= input.NowMS {
			return AdmissionWarmSample, false, ""
		}
		return AdmissionDegraded, false, ""
	case QualityWarming:
		if hasFreshSuccessfulEvidence(quality, input.NowMS, input.RecoveryEvidenceTTLMS) {
			return AdmissionWarming, false, ""
		}
		// The single-flight sample lane remains available during the profile
		// observation period so low-traffic routes can still accumulate evidence.
		if quality.SampleDueAtMS <= input.NowMS {
			return AdmissionWarmSample, false, ""
		}
		if recoveredRouteCooling(quality, input) {
			return "", false, RejectRecoveryCooling
		}
		admissionKey := input.Request.AdmissionKey
		if admissionKey == "" {
			// Preserve compatibility for direct planner callers that predate the
			// explicit admission key. Runtime sessions always fill it in.
			admissionKey = input.Request.WarmingKey
		}
		if DeterministicAdmission(admissionKey, route.RouteID, quality.WarmingEpoch, quality.WarmingTrafficPPM) {
			return AdmissionWarming, false, ""
		}
		return "", false, RejectHealthUnavailable
	case QualityUnknown:
		if quality.NextProbeAtMS <= input.NowMS {
			return AdmissionProbe, false, ""
		}
		return "", false, RejectHealthUnavailable
	case QualityOpen:
		// Open is a v3 shared-state encoding. In v4 it is interpreted through
		// this key's threshold, so a more tolerant key may still use the route.
		return AdmissionDegraded, false, ""
	case QualityHalfOpen:
		return "", false, RejectHealthUnavailable
	default:
		return "", false, RejectHealthUnavailable
	}
}

func hasFreshSuccessfulEvidence(quality QualityState, nowMS, ttlMS int64) bool {
	if ttlMS <= 0 || quality.LastSuccessMS <= 0 || quality.LastSuccessMS <= quality.LastFailureMS {
		return false
	}
	return nowMS <= saturatingAdd(quality.LastSuccessMS, ttlMS)
}

func recoveredRouteCooling(quality QualityState, input FilterInput) bool {
	// In strict price mode the canary/health state is the recovery gate. A
	// second fixed 5/30-minute delay would keep an already admitted cheaper
	// route hidden and violate the policy's observable price-first contract.
	if input.Policy.EffectiveStrategy() == StrategyPrice {
		return false
	}
	if quality.WarmingEpoch == 0 {
		return false
	}
	delayMS := recoveryAdmissionDelayMS(input.Policy.RecoveryProfile)
	if delayMS == 0 {
		return false
	}
	if quality.StableSinceMS <= 0 {
		return true
	}
	return input.NowMS < saturatingAdd(quality.StableSinceMS, delayMS)
}

func recoveryAdmissionDelayMS(profile RecoveryProfile) int64 {
	switch profile {
	case RecoveryFast:
		return 0
	case RecoveryBalanced:
		return 5 * 60 * 1_000
	case RecoveryStable:
		return 30 * 60 * 1_000
	default:
		return 0
	}
}

func strongAffinityMatches(affinity *AffinityState, contractID string, route CertifiedRoute) bool {
	return affinity != nil && affinity.Strength == AffinityStrong && affinity.ContractID == contractID &&
		affinity.RouteID == route.RouteID && affinity.ChannelID == route.ChannelID
}

func applyTTFTKnowledge(candidate *Candidate, state TTFTState, policy TTFTPolicy, nowMS int64) {
	if !policy.Enabled {
		candidate.TTFTKnowledge = TTFTDisabled
		return
	}
	if state.Samples < policy.MinSamples || state.P95MS <= 0 || state.FreshUntilMS <= nowMS {
		candidate.TTFTKnowledge = TTFTUnknown
		return
	}
	candidate.TTFTKnowledge = TTFTKnown
	candidate.TTFTP95MS = state.P95MS
	candidate.TTFTSlow = state.P95MS > policy.HardMaxMS
	candidate.TTFTAboveTarget = state.P95MS > policy.TargetMS
}

func applyTTFTPolicy(candidates []Candidate, policy TTFTPolicy, rejections *[]Rejection) ([]Candidate, bool) {
	if !policy.Enabled || len(candidates) == 0 {
		return candidates, false
	}
	hasAcceptable := false
	for _, candidate := range candidates {
		if !candidate.TTFTSlow {
			hasAcceptable = true
			break
		}
	}
	if !hasAcceptable {
		return candidates, true
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if candidate.TTFTSlow {
			*rejections = append(*rejections, Rejection{RouteID: candidate.Route.RouteID, Reason: RejectTTFTSlow})
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered, false
}

func sortCandidates(candidates []Candidate, strategy Strategy, ttftFallback, backgroundRecovery bool) {
	sort.SliceStable(candidates, func(leftIndex, rightIndex int) bool {
		left := candidates[leftIndex]
		right := candidates[rightIndex]
		if backgroundRecovery {
			leftLastResort := left.Admission == AdmissionLastResort
			rightLastResort := right.Admission == AdmissionLastResort
			if leftLastResort != rightLastResort {
				return !leftLastResort
			}
		}
		if ttftFallback && left.TTFTP95MS != right.TTFTP95MS {
			return left.TTFTP95MS < right.TTFTP95MS
		}
		if left.TTFTAboveTarget != right.TTFTAboveTarget {
			return !left.TTFTAboveTarget
		}
		switch strategy {
		case StrategyManual:
			if left.ManualIndex != right.ManualIndex {
				return left.ManualIndex < right.ManualIndex
			}
			if left.StabilityPPM != right.StabilityPPM {
				return left.StabilityPPM > right.StabilityPPM
			}
		case StrategyStability:
			if left.StabilityPPM != right.StabilityPPM {
				return left.StabilityPPM > right.StabilityPPM
			}
			if left.Quality.ConsecutiveHardFailures != right.Quality.ConsecutiveHardFailures {
				return left.Quality.ConsecutiveHardFailures < right.Quality.ConsecutiveHardFailures
			}
			if comparison, comparable := comparableCandidatePrice(left, right); comparable && comparison != 0 {
				return comparison < 0
			}
		case StrategyLatency:
			leftKnown := left.TTFTKnowledge == TTFTKnown
			rightKnown := right.TTFTKnowledge == TTFTKnown
			if leftKnown != rightKnown {
				return leftKnown
			}
			if leftKnown && left.TTFTP95MS != right.TTFTP95MS {
				return left.TTFTP95MS < right.TTFTP95MS
			}
			if left.StabilityPPM != right.StabilityPPM {
				return left.StabilityPPM > right.StabilityPPM
			}
			if comparison, comparable := comparableCandidatePrice(left, right); comparable && comparison != 0 {
				return comparison < 0
			}
		case StrategyBalanced:
			if left.BalancedScorePPM != right.BalancedScorePPM {
				return left.BalancedScorePPM > right.BalancedScorePPM
			}
			if comparison, comparable := comparableCandidatePrice(left, right); comparable && comparison != 0 {
				return comparison < 0
			}
		default: // StrategyPrice
			if left.priceClassRank != right.priceClassRank {
				return left.priceClassRank < right.priceClassRank
			}
			if comparison, comparable := comparableCandidatePrice(left, right); comparable && comparison != 0 {
				return comparison < 0
			}
			if left.StabilityPPM != right.StabilityPPM {
				return left.StabilityPPM > right.StabilityPPM
			}
		}
		if left.PoolIndex != right.PoolIndex {
			return left.PoolIndex < right.PoolIndex
		}
		if left.Route.RouteID != right.Route.RouteID {
			return left.Route.RouteID < right.Route.RouteID
		}
		return left.Route.ChannelID < right.Route.ChannelID
	})
}

func qualityStabilityPPM(state QualityState) int64 {
	base := state.ReliabilityPPM
	if state.ReliabilitySamples == 0 {
		base = 500_000
	}
	switch state.Phase {
	case QualityHealthy:
		base += 150_000
	case QualityBootstrap, QualityUnknown:
		base -= 50_000
	case QualityDegraded, QualityOpen, QualityHalfOpen:
		base -= 250_000
	case QualityQuarantined:
		return 0
	}
	base -= int64(state.ConsecutiveHardFailures) * 50_000
	if base < 0 {
		return 0
	}
	if base > 1_000_000 {
		return 1_000_000
	}
	return base
}

func assignBalancedScores(candidates []Candidate, weights BalancedWeights) {
	if len(candidates) == 0 {
		return
	}
	priceComparable := candidates[0].Price.StaticComparable
	priceClass := candidates[0].Price.ComparisonClass
	minPrice, maxPrice := candidates[0].Price.ScorePPM, candidates[0].Price.ScorePPM
	minTTFT, maxTTFT := int64(0), int64(0)
	for _, candidate := range candidates {
		if !candidate.Price.StaticComparable || candidate.Price.ComparisonClass != priceClass {
			priceComparable = false
		}
		if candidate.Price.ScorePPM < minPrice {
			minPrice = candidate.Price.ScorePPM
		}
		if candidate.Price.ScorePPM > maxPrice {
			maxPrice = candidate.Price.ScorePPM
		}
		if candidate.TTFTKnowledge == TTFTKnown {
			if minTTFT == 0 || candidate.TTFTP95MS < minTTFT {
				minTTFT = candidate.TTFTP95MS
			}
			if candidate.TTFTP95MS > maxTTFT {
				maxTTFT = candidate.TTFTP95MS
			}
		}
	}
	for index := range candidates {
		priceScore := int64(500_000)
		if priceComparable {
			priceScore = inverseRangeScore(candidates[index].Price.ScorePPM, minPrice, maxPrice)
		}
		ttftScore := int64(500_000)
		if candidates[index].TTFTKnowledge == TTFTKnown {
			ttftScore = inverseRangeScore(candidates[index].TTFTP95MS, minTTFT, maxTTFT)
		}
		candidates[index].BalancedScorePPM =
			(candidates[index].StabilityPPM*int64(weights.Stability) +
				priceScore*int64(weights.Price) + ttftScore*int64(weights.TTFT)) / 100
	}
}

func inverseRangeScore(value, minimum, maximum int64) int64 {
	if maximum <= minimum {
		return 1_000_000
	}
	if value <= minimum {
		return 1_000_000
	}
	if value >= maximum {
		return 0
	}
	return (maximum - value) * 1_000_000 / (maximum - minimum)
}

func RouteCredentialDomain(route CertifiedRoute) string {
	if route.CredentialDomain != "" {
		return route.CredentialDomain
	}
	return fmt.Sprintf("channel:%d:%s", route.ChannelID, route.ChannelGeneration)
}

func applyAffinity(
	candidates []Candidate,
	affinity *AffinityState,
	economy *CacheEconomySnapshot,
	estimatedContext int,
	contractID string,
	weakTolerancePPM int64,
	maxPremiumPercent int,
	affinityMode AffinityMode,
	strategy Strategy,
	healthGuard bool,
) ([]Candidate, bool, CacheEconomyDecision) {
	economyDecision := fallbackCacheEconomyDecision(CacheEconomyReasonDisabled, economy)
	if affinity == nil || affinity.RouteID == "" || affinity.ContractID == "" || affinity.ContractID != contractID {
		economyDecision.Reason = CacheEconomyReasonNoAffinity
		return candidates, false, economyDecision
	}
	matchIndex := -1
	for index, candidate := range candidates {
		if candidate.Route.RouteID != affinity.RouteID || candidate.Route.ChannelID != affinity.ChannelID {
			continue
		}
		if healthGuard && candidate.Quality.Phase != QualityHealthy {
			continue
		}
		matchIndex = index
		break
	}
	if matchIndex < 0 {
		return candidates, false, economyDecision
	}
	if affinity.Strength == AffinityWeak {
		if strategy == StrategyManual {
			return candidates, matchIndex == 0, economyDecision
		}
		matchedPrice, selectedPrice, comparable := comparableCandidatePriceScores(candidates[matchIndex], candidates[0])
		if len(candidates) == 0 || !comparable || matchedPrice > saturatingAdd(selectedPrice, weakTolerancePPM) {
			return candidates, true, economyDecision
		}
		result := append([]Candidate(nil), candidates...)
		matched := result[matchIndex]
		copy(result[1:matchIndex+1], result[0:matchIndex])
		result[0] = matched
		return result, true, economyDecision
	}
	if affinity.Strength != AffinityStrong {
		return candidates, false, economyDecision
	}
	// Explicit stability, latency, balanced, and manual policies own the
	// ordering. Strong cache affinity may be preserved when it already wins,
	// but must not reorder a candidate above that policy's selected route.
	if strategy != StrategyPrice {
		return candidates, matchIndex == 0, economyDecision
	}
	if affinityMode == AffinityModeEconomicBreakEven && len(candidates) > 0 &&
		!isRecoveryAttemptAdmission(candidates[0].Admission) {
		matchedPrice := candidateRoutePrice(candidates[matchIndex])
		selectedPrice := candidateRoutePrice(candidates[0])
		if matchedPrice.Synthetic || selectedPrice.Synthetic {
			economyDecision.Reason = CacheEconomyReasonSyntheticPrice
		} else {
			economyDecision = EvaluateCacheEconomy(economy, estimatedContext, candidates[matchIndex], candidates[0])
			switch economyDecision.Action {
			case CacheEconomySwitch:
				return candidates, false, economyDecision
			case CacheEconomyKeep:
				result := make([]Candidate, 0, len(candidates))
				matched := candidates[matchIndex]
				result = append(result, matched)
				for index, candidate := range candidates {
					if index != matchIndex && candidate.Route.CacheNamespaceIdentity() == matched.Route.CacheNamespaceIdentity() {
						result = append(result, candidate)
					}
				}
				for index, candidate := range candidates {
					if index != matchIndex && candidate.Route.CacheNamespaceIdentity() != matched.Route.CacheNamespaceIdentity() {
						result = append(result, candidate)
					}
				}
				return result, true, economyDecision
			}
		}
	}
	matched := candidates[matchIndex]
	if matchIndex != 0 && !matched.Price.ComparableWith(candidates[0].Price) {
		return candidates, false, economyDecision
	}
	minimumPrice := matched.Price.ScorePPM
	for _, candidate := range candidates {
		if matched.Price.ComparableWith(candidate.Price) && candidate.Price.ScorePPM < minimumPrice {
			minimumPrice = candidate.Price.ScorePPM
		}
	}
	if !affinityWithinPremium(matched.Price.ScorePPM, minimumPrice, maxPremiumPercent) {
		// Cache affinity is valuable, but it must not silently defeat a user's
		// configured price ceiling. Leave normal strategy ordering intact and
		// let a successful request replace the stale affinity record.
		return candidates, false, economyDecision
	}
	result := make([]Candidate, 0, len(candidates))
	result = append(result, matched)
	for index, candidate := range candidates {
		if index != matchIndex && candidate.Route.CacheDomain == matched.Route.CacheDomain {
			result = append(result, candidate)
		}
	}
	for index, candidate := range candidates {
		if index != matchIndex && candidate.Route.CacheDomain != matched.Route.CacheDomain {
			result = append(result, candidate)
		}
	}
	return result, true, economyDecision
}

func affinityWithinPremium(matchedRatio, minimumRatio int64, maxPremiumPercent int) bool {
	if matchedRatio < 0 || minimumRatio < 0 || maxPremiumPercent < 0 {
		return false
	}
	if matchedRatio <= minimumRatio {
		return true
	}
	if minimumRatio == 0 {
		return false
	}
	return matchedRatio-minimumRatio <= minimumRatio*int64(maxPremiumPercent)/100
}

func affinityDisposition(affinity *AffinityState, selected Candidate, affinityEligible bool) AffinityDisposition {
	if affinity == nil {
		return AffinityNone
	}
	if affinity.Strength == AffinityStrong && affinityEligible {
		return AffinityPreserve
	}
	if affinity.RouteID == selected.Route.RouteID && affinity.ChannelID == selected.Route.ChannelID {
		return AffinityPreserve
	}
	return AffinityReplaceOnSuccess
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func intSet(values []int) map[int]bool {
	result := make(map[int]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func routeRanks(routes []string) map[string]int {
	result := make(map[string]int, len(routes))
	for index, routeID := range routes {
		if _, exists := result[routeID]; !exists {
			result[routeID] = index
		}
	}
	return result
}

func hasRouteRank(ranks map[string]int, routeID string) bool {
	_, ok := ranks[routeID]
	return ok
}
