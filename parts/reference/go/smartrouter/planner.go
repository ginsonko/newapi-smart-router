package smartrouter

import (
	"errors"
	"fmt"
	"sort"
	"strings"
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
	// UsageSemantic separates OpenAI-style and Anthropic-style cache fields in
	// the shared learner. It is internal and never supplied by the client.
	UsageSemanticValue string             `json:"-"`
	DeadlineMS         int64              `json:"deadline_ms,omitempty"`
	Media              *MediaRequestShape `json:"media,omitempty"`
}

func (request RouteRequest) UsageSemantic() string {
	if value := strings.TrimSpace(request.UsageSemanticValue); value != "" {
		return value
	}
	endpoint := strings.ToLower(strings.TrimSpace(request.Endpoint))
	if strings.Contains(endpoint, "anthropic") || strings.Contains(endpoint, "messages") {
		return "anthropic"
	}
	return "openai"
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
	RecoveryFallbackEnabled  bool
	AutomaticExcludedRoutes  []string
	TextRecoveryProbeAfterMS int64
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
	ActualInputCosts         ActualInputCostSnapshot
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
	RecoveryFallback          bool
	Route                     CertifiedRoute
	RatioPPM                  int64
	Price                     RoutePrice
	PoolIndex                 int
	ManualIndex               int
	Quality                   QualityState
	Credential                CredentialState
	CredentialDomain          string
	Admission                 AdmissionKind
	KeySuppressed             bool
	StabilityPPM              int64
	BalancedScorePPM          int64
	CustomScorePPM            int64
	TTFTKnowledge             TTFTKnowledge
	TTFTP95MS                 int64
	TTFTSlow                  bool
	TTFTAboveTarget           bool
	ActualInputCostPPM        int64
	ActualInputCostSource     string
	ActualInputCostObservedMS int64
	ActualInputCostComparable bool
	RoutingCacheWeightPPM     int64
	RoutingCacheWeightKnown   bool
	priceClassRank            int
	mediaQuoteRank            uint8
}

const (
	mediaQuoteNone uint8 = iota
	mediaQuoteKnown
	mediaQuoteUnknown
)

type RejectReason string

const (
	RejectGroupUnauthorized         RejectReason = "group_unauthorized"
	RejectGroupNotSelected          RejectReason = "group_not_selected"
	RejectGroupExcluded             RejectReason = "group_excluded"
	RejectRouteExcluded             RejectReason = "route_excluded"
	RejectAlreadyAttempted          RejectReason = "already_attempted"
	RejectChannelAttempted          RejectReason = "channel_already_attempted"
	RejectPriceMissing              RejectReason = "price_missing"
	RejectAbovePriceLimit           RejectReason = "above_price_limit"
	RejectCapabilityMismatch        RejectReason = "capability_mismatch"
	RejectContextTooLarge           RejectReason = "context_too_large"
	RejectHealthUnavailable         RejectReason = "health_unavailable"
	RejectRecoveryBudget            RejectReason = "recovery_budget_exhausted"
	RejectRecoveryCooling           RejectReason = "recovery_cooling"
	RejectKeySuppressed             RejectReason = "key_suppressed"
	RejectQuarantined               RejectReason = "quarantined"
	RejectCredentialBlocked         RejectReason = "credential_blocked"
	RejectUnlistedManualRoute       RejectReason = "unlisted_manual_route"
	RejectTTFTSlow                  RejectReason = "ttft_slow"
	RejectMediaReferenceUnsupported RejectReason = "media_reference_unsupported"
	RejectMediaReferenceRole        RejectReason = "media_reference_role_unsupported"
	RejectMediaReferenceLimit       RejectReason = "media_reference_limit"
	RejectMediaDurationMismatch     RejectReason = "media_duration_mismatch"
	RejectMediaOutputCountMismatch  RejectReason = "media_output_count_mismatch"
	RejectMediaResolutionMismatch   RejectReason = "media_resolution_mismatch"
	RejectMediaAspectRatioMismatch  RejectReason = "media_aspect_ratio_mismatch"
	RejectMediaQualityMismatch      RejectReason = "media_quality_mismatch"
	RejectMediaPriceUnavailable     RejectReason = "media_request_price_unavailable"
)

type Rejection struct {
	RouteID string       `json:"route_id"`
	Reason  RejectReason `json:"reason"`
}

type FilterInput struct {
	RecoveryFallbackEnabled  bool
	AutomaticExcludedRoutes  []string
	RecoveryFallback         bool
	TextRecoveryProbeAfterMS int64
	NowMS                    int64
	RecoveryEvidenceTTLMS    int64
	Request                  RouteRequest
	Policy                   Policy
	Catalog                  CertifiedRouteCatalog
	Prices                   PriceSnapshot
	Quality                  QualitySnapshot
	TTFT                     TTFTSnapshot
	Credentials              CredentialSnapshot
	AllowedGroups            []string
	Affinity                 *AffinityState
	ActualInputCosts         ActualInputCostSnapshot
	Attempt                  AttemptState
	BackgroundRecovery       bool
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
	RecoveryFallback    bool
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
	// Safe text is replayable until every remaining certified physical channel
	// has been tried. A legacy numeric attempt cap must not collapse that
	// candidate set during rolling upgrades. Media/side-effecting requests
	// retain their explicit bounded dispatch contract.
	if input.Attempt.StartedAttempts < 0 ||
		(input.Request.ReplayClass != ReplaySafeText &&
			(input.Attempt.MaxAttempts <= 0 ||
				(input.Attempt.StartedAttempts >= input.Attempt.MaxAttempts && !input.Attempt.ExhaustEligibleChannels))) {
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
	automaticExcluded := stringSet(input.AutomaticExcludedRoutes)
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
	lastResort := make([]Candidate, 0)
	lastResortReasons := make([]Rejection, 0)

	for index, route := range pool.Candidates {
		useLastResort := false
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
		case route.Capabilities&input.Request.RequiredCapabilities != input.Request.RequiredCapabilities:
			reason = RejectCapabilityMismatch
		case input.Request.EstimatedContext < 0 || input.Request.EstimatedContext > route.MaxContext:
			reason = RejectContextTooLarge
		case orderMode == OrderManual && !input.Policy.AllowFutureCertifiedRoutes:
			if len(manualRanks) > 0 && !hasRouteRank(manualRanks, route.RouteID) {
				reason = RejectUnlistedManualRoute
			} else if len(manualRanks) == 0 && len(manualGroupRanks) > 0 && !hasRouteRank(manualGroupRanks, route.Group) {
				reason = RejectUnlistedManualRoute
			}
		}
		if reason == "" && input.Request.Media != nil {
			reason = MatchMediaRequest(route.Media, input.Request.Media)
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
		if automaticExcluded[route.RouteID] {
			if !input.RecoveryFallback {
				result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: RejectRouteExcluded})
				continue
			}
			useLastResort = true
		}
		routePrice := routePriceForCandidate(input.Prices, route.RouteID, ratio)
		if input.Request.Media != nil {
			var quoteReason RejectReason
			routePrice, quoteReason = routePrice.QuoteMedia(input.Request.Media)
			if quoteReason != "" {
				result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: quoteReason})
				continue
			}
		}

		quality, found := input.Quality.Routes[route.RouteID]
		if !found {
			quality = InitialQuality(route.StableFallback)
		}
		if input.Request.ReplayClass == ReplaySafeImage {
			quality, _ = NormalizeImageQuality(quality, input.NowMS, QualityConfig{})
		} else {
			quality, _ = NormalizeAutomaticRecoveryState(quality, input.NowMS)
		}
		credentialDomain := RouteCredentialDomain(route)
		credential := input.Credentials.Domains[credentialDomain]
		if credential.BlockedUntilMS > input.NowMS {
			if input.Request.ReplayClass == ReplaySafeImage || !input.RecoveryFallback {
				result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: RejectCredentialBlocked})
				continue
			}
			useLastResort = true
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
		// Safe stateless text may walk every remaining certified physical route.
		// RecoveryProbeUsed is a single-dispatch guard for media/side-effecting
		// work, not a global foreground gate: applying it to text was the source
		// of the R54 "no available channel" collapse after routes entered warming.
		if healthRejection == "" && isRecoveryAttemptAdmission(admission) &&
			input.Attempt.RecoveryProbeUsed && (input.Request.ReplayClass != ReplaySafeText || route.PassiveRecovery) && !mediaVirginRetry {
			healthRejection = RejectRecoveryBudget
		}
		if healthRejection != "" {
			// A safe text request may use a route with stale/open health only
			// after all currently healthy candidates have been consumed. Keep
			// the route out of the first plan, but do not lose it permanently;
			// the next planner pass (after healthy failures are recorded) will
			// expose it as the last-resort candidate.
			if input.Request.ReplayClass == ReplaySafeImage || input.RecoveryFallback || (!input.RecoveryFallbackEnabled && input.Request.ReplayClass == ReplaySafeText && !route.PassiveRecovery && input.TextRecoveryProbeAfterMS == 0 && !quality.DemandRecovery) {
				useLastResort = true
				admission = AdmissionLastResort
				suppressed = false
				lastResortReasons = append(lastResortReasons, Rejection{RouteID: route.RouteID, Reason: healthRejection})
				healthRejection = ""
			} else {
				result.Rejections = append(result.Rejections, Rejection{RouteID: route.RouteID, Reason: healthRejection})
				continue
			}
		}
		if useLastResort {
			admission = AdmissionLastResort
			suppressed = false
			bootstrapUnknown = false
		}
		manualIndex := manualCandidateIndex(route, index, len(pool.Candidates), manualRanks, manualGroupRanks)
		priceClassRank := 0
		mediaQuoteRank := mediaQuoteNone
		if input.Request.Media != nil {
			mediaQuoteRank = mediaQuoteKnown
			// All comparable media quotes share one request-cost unit. Unknown
			// prices remain usable fallbacks but can never outrank a known quote.
			if !routePrice.StaticComparable {
				mediaQuoteRank = mediaQuoteUnknown
				priceClassRank = 1 + index
			}
		} else {
			priceClass := routePrice.ComparisonClass
			if !routePrice.StaticComparable {
				priceClass = "incomparable:" + route.RouteID
			}
			var exists bool
			priceClassRank, exists = priceClassRanks[priceClass]
			if !exists {
				priceClassRank = len(priceClassRanks)
				priceClassRanks[priceClass] = priceClassRank
			}
		}
		candidate := Candidate{
			RecoveryFallback: input.RecoveryFallback && useLastResort,
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
			mediaQuoteRank:   mediaQuoteRank,
		}
		if input.Request.Media == nil {
			actualKey := ActualInputCostKey(
				route.CacheNamespaceIdentity(), route.UpstreamModel,
				input.Request.CanonicalModel, input.Request.Endpoint, input.Request.UsageSemantic(),
			)
			poolKey := ActualInputCostPoolKey(
				route.UpstreamModel, input.Request.CanonicalModel, input.Request.Endpoint, input.Request.UsageSemantic(),
			)
			actual := EstimateActualInputCostAt(routePrice, routePrice.ScorePPM, actualKey, poolKey, input.ActualInputCosts, input.NowMS)
			candidate.ActualInputCostPPM = actual.EffectivePPM
			candidate.ActualInputCostSource = actual.Source
			candidate.ActualInputCostObservedMS = actual.LastObservedAtMS
			candidate.ActualInputCostComparable = actual.Comparable
			candidate.RoutingCacheWeightPPM = actual.CacheReadRatePPM
			candidate.RoutingCacheWeightKnown = actual.Comparable && actualInputCostCacheWeightKnown(actual.Source)
		}
		applyTTFTKnowledge(&candidate, input.TTFT.Routes[route.RouteID], metricPolicy, input.NowMS)
		if bootstrapUnknown {
			virginBootstrap = append(virginBootstrap, candidate)
		} else if useLastResort {
			lastResort = append(lastResort, candidate)
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
	if ((input.RecoveryFallback && input.Request.ReplayClass != ReplaySafeImage) || len(result.Candidates) == 0) && len(lastResort) > 0 {
		result.Candidates = append(result.Candidates, lastResort...)
	} else {
		result.Rejections = append(result.Rejections, lastResortReasons...)
	}

	result.Candidates, result.TTFTFallback = applyTTFTPolicy(result.Candidates, input.Policy.TTFTPolicy, &result.Rejections)
	actualCostRanking := input.Policy.EffectiveActualInputCostRanking()
	assignBalancedScores(result.Candidates, input.Policy.EffectiveBalancedWeights(), actualCostRanking)
	assignCustomScores(result.Candidates, input.Policy.EffectiveCustomWeights(), actualCostRanking)
	sortCandidates(result.Candidates, strategy, result.TTFTFallback, input.BackgroundRecovery, actualCostRanking)
	result.Candidates = promoteUnknownProbeCandidate(result.Candidates, input.Request, strategy)
	// A safe text request can be replayed before semantic output and must try
	// every remaining eligible physical channel once. Recovery admission remains
	// single-flight for media/audio, where a second dispatch could duplicate a
	// task or charge.
	if input.Request.ReplayClass != ReplaySafeText {
		result.Candidates = limitRecoveryProbeCandidates(
			result.Candidates,
			&result.Rejections,
			false,
		)
	}
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
	if strategy != StrategyLatency && strategy != StrategyBalanced && strategy != StrategyCustomWeighted {
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
		if candidates[0].mediaQuoteRank == mediaQuoteKnown && candidate.mediaQuoteRank == mediaQuoteUnknown {
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
	if policy.Enabled || (strategy != StrategyLatency && strategy != StrategyBalanced && strategy != StrategyCustomWeighted) {
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
	result, err := planRecoveryPass(input, false)
	if err == nil || !input.RecoveryFallbackEnabled ||
		(!errors.Is(err, ErrNoCandidate) && !errors.Is(err, ErrCapacityConstrained)) {
		return result, err
	}
	// Another media submission requires the controller's existing proof that
	// the previous attempt was not accepted. Planning never creates that proof.
	if isReplaySafeMedia(input.Request.ReplayClass) && input.Attempt.StartedAttempts > 0 &&
		!input.Attempt.VirginMediaRetryAuthorized {
		return result, err
	}
	return planRecoveryPass(input, true)
}

func planRecoveryPass(input PlanInput, recoveryFallback bool) (PlanResult, error) {
	filtered, err := Filter(FilterInput{
		RecoveryFallbackEnabled:  input.RecoveryFallbackEnabled,
		AutomaticExcludedRoutes:  input.AutomaticExcludedRoutes,
		RecoveryFallback:         recoveryFallback,
		TextRecoveryProbeAfterMS: input.TextRecoveryProbeAfterMS,
		NowMS:                    input.NowMS,
		RecoveryEvidenceTTLMS:    input.RecoveryEvidenceTTLMS,
		Request:                  input.Request,
		Policy:                   input.Policy,
		Catalog:                  input.Catalog,
		Prices:                   input.Prices,
		Quality:                  input.Quality,
		TTFT:                     input.TTFT,
		Credentials:              input.Credentials,
		AllowedGroups:            input.AllowedGroups,
		Affinity:                 input.Affinity,
		ActualInputCosts:         input.ActualInputCosts,
		Attempt:                  input.Attempt,
		BackgroundRecovery:       input.BackgroundRecovery,
	})
	if err != nil {
		return PlanResult{Candidates: filtered.Candidates, Rejections: filtered.Rejections}, err
	}
	if recoveryFallback {
		for i := range filtered.Candidates {
			candidate := &filtered.Candidates[i]
			if input.Capacity.Domains[candidate.Route.CapacityDomain].BlockedUntilMS > input.NowMS {
				candidate.Admission = AdmissionLastResort
				candidate.RecoveryFallback = true
			}
		}
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
	if input.Request.ReplayClass == ReplaySafeImage && choice.Candidate.Admission == AdmissionLastResort &&
		input.Attempt.StartedAttempts > 0 && !input.Attempt.VirginMediaRetryAuthorized {
		return PlanResult{Candidates: ordered, Rejections: filtered.Rejections}, ErrUnsupportedReplay
	}
	disposition := affinityDisposition(input.Affinity, choice.Candidate, affinityEligible)
	overflow := input.Affinity != nil && input.Affinity.Strength == AffinityStrong && affinityEligible && choice.Candidate.Route.RouteID != input.Affinity.RouteID
	maxInflightHint := admissionCapacityLimit(choice.Candidate.Admission)
	return PlanResult{
		RecoveryFallback:    choice.Candidate.RecoveryFallback,
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
		MaxInflightHint:     maxInflightHint,
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
	if input.Request.ReplayClass == ReplaySafeImage {
		if quality.ImageUnavailable {
			return "", false, RejectHealthUnavailable
		}
		return AdmissionNormal, false, ""
	}
	if route.PassiveRecovery || isReplaySafeMedia(input.Request.ReplayClass) {
		if quality.Phase == QualityHalfOpen {
			return "", false, RejectHealthUnavailable
		}
		if quality.Phase == QualityOpen {
			if quality.NextProbeAtMS <= input.NowMS {
				return AdmissionProbe, false, ""
			}
			return "", false, RejectHealthUnavailable
		}
	}
	if input.TextRecoveryProbeAfterMS > 0 && !route.PassiveRecovery && !isReplaySafeMedia(input.Request.ReplayClass) {
		if quality.Phase == QualityHalfOpen {
			return "", false, RejectHealthUnavailable
		}
		if quality.Phase == QualityOpen {
			started := quality.RecoveryStartedMS
			if started == 0 {
				started = quality.FirstFailureMS
			}
			if started == 0 {
				started = quality.LastFailureMS
			}
			if started > 0 && input.NowMS < saturatingAdd(started, input.TextRecoveryProbeAfterMS) && quality.NextProbeAtMS <= input.NowMS {
				return AdmissionProbe, false, ""
			}
			return "", false, RejectHealthUnavailable
		}
	}
	if !input.Policy.HealthGuard {
		return AdmissionNormal, false, ""
	}
	switch quality.Phase {
	case QualityUnknown, QualityBootstrap, QualityHealthy:
		return AdmissionNormal, false, ""
	case QualityWarming:
		// Filter normally normalizes this legacy phase. Preserve the same
		// evidence rule here for direct callers during rolling upgrades.
		if quality.LastSuccessMS > quality.LastFailureMS {
			return AdmissionNormal, false, ""
		}
		return "", true, RejectHealthUnavailable
	case QualityDegraded:
		// One or two distinct confirmed failures lower stability but do not
		// globally remove a route. The normal request-local confirmation and
		// fallback sequence still protects the caller if the fluctuation persists.
		return AdmissionDegraded, false, ""
	case QualityOpen, QualityHalfOpen, QualityQuarantined:
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
	// Recovery profiles are telemetry/ranking hints only. A successful probe
	// already transitions the route to healthy; no cooling or warm-up timer may
	// hide it from the next foreground request.
	_ = quality
	_ = input
	return false
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

func sortCandidates(candidates []Candidate, strategy Strategy, ttftFallback, backgroundRecovery, actualCostRanking bool) {
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
		if left.mediaQuoteRank != mediaQuoteNone && right.mediaQuoteRank != mediaQuoteNone &&
			left.mediaQuoteRank != right.mediaQuoteRank {
			return left.mediaQuoteRank < right.mediaQuoteRank
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
			if comparison, comparable := comparableCandidateOrderingPrice(left, right, actualCostRanking); comparable && comparison != 0 {
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
			if comparison, comparable := comparableCandidateOrderingPrice(left, right, actualCostRanking); comparable && comparison != 0 {
				return comparison < 0
			}
		case StrategyBalanced:
			if left.BalancedScorePPM != right.BalancedScorePPM {
				return left.BalancedScorePPM > right.BalancedScorePPM
			}
			if comparison, comparable := comparableCandidateOrderingPrice(left, right, actualCostRanking); comparable && comparison != 0 {
				return comparison < 0
			}
		case StrategyCustomWeighted:
			if left.CustomScorePPM != right.CustomScorePPM {
				return left.CustomScorePPM > right.CustomScorePPM
			}
			if comparison, comparable := comparableCandidateOrderingPrice(left, right, actualCostRanking); comparable && comparison != 0 {
				return comparison < 0
			}
		default: // StrategyPrice
			actualCompared := false
			if actualCostRanking {
				leftObserved := candidateHasActualInputCost(left)
				rightObserved := candidateHasActualInputCost(right)
				// A measured formula result is preferred to an unobserved
				// candidate. Static price may still break ties between two
				// unknown routes for deterministic availability, but it is never
				// compared against (or exposed as) the real-input prediction.
				if leftObserved != rightObserved {
					return leftObserved
				}
				if comparison, comparable := comparableCandidateActualInputCost(left, right); comparable {
					actualCompared = true
					if comparison != 0 {
						return comparison < 0
					}
				}
			}
			if !actualCompared {
				if left.priceClassRank != right.priceClassRank {
					return left.priceClassRank < right.priceClassRank
				}
				if comparison, comparable := comparableCandidatePrice(left, right); comparable && comparison != 0 {
					return comparison < 0
				}
			}
			if actualCompared {
				// Evidence priority only distinguishes two otherwise equal
				// observed values; it cannot manufacture a price for unknown data.
				if left.ActualInputCostObservedMS != right.ActualInputCostObservedMS {
					return left.ActualInputCostObservedMS > right.ActualInputCostObservedMS
				}
			}
			if left.StabilityPPM != right.StabilityPPM {
				return left.StabilityPPM > right.StabilityPPM
			}
		}
		if actualCostRanking {
			if comparison, comparable := comparableCandidateActualInputCost(left, right); comparable && comparison == 0 {
				if evidenceComparison := compareActualInputCostEvidencePriority(left, right); evidenceComparison != 0 {
					return evidenceComparison < 0
				}
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

func actualInputCostCacheWeightKnown(source string) bool {
	return source == ActualInputCostObserved || source == ActualInputCostHistorical ||
		source == ActualInputCostOptimistic
}

func comparableCandidateOrderingPrice(left, right Candidate, actualCostRanking bool) (int, bool) {
	if actualCostRanking {
		return comparableCandidateActualInputCost(left, right)
	}
	return comparableCandidatePrice(left, right)
}

func comparableCandidateActualInputCost(left, right Candidate) (int, bool) {
	leftPrice := candidateRoutePrice(left)
	rightPrice := candidateRoutePrice(right)
	if candidateHasActualInputCost(left) && candidateHasActualInputCost(right) &&
		leftPrice.ActualInputComparisonClass != "" &&
		leftPrice.ActualInputComparisonClass == rightPrice.ActualInputComparisonClass {
		switch {
		case left.ActualInputCostPPM < right.ActualInputCostPPM:
			return -1, true
		case left.ActualInputCostPPM > right.ActualInputCostPPM:
			return 1, true
		default:
			return 0, true
		}
	}
	return 0, false
}

func candidateHasActualInputCost(candidate Candidate) bool {
	price := candidateRoutePrice(candidate)
	return candidate.ActualInputCostComparable &&
		(candidate.ActualInputCostSource == ActualInputCostObserved ||
			candidate.ActualInputCostSource == ActualInputCostHistorical ||
			candidate.ActualInputCostSource == ActualInputCostOptimistic) &&
		candidate.ActualInputCostPPM >= 0 &&
		price.ActualInputComparisonClass != ""
}

func compareActualInputCostEvidencePriority(left, right Candidate) int {
	if candidateHasActualInputCost(left) && candidateHasActualInputCost(right) &&
		left.ActualInputCostObservedMS != right.ActualInputCostObservedMS {
		if left.ActualInputCostObservedMS < right.ActualInputCostObservedMS {
			return -1
		}
		return 1
	}
	return 0
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

func assignBalancedScores(candidates []Candidate, weights BalancedWeights, actualCostRanking bool) {
	if len(candidates) == 0 {
		return
	}
	firstPrice, priceComparable := candidateOrderingPrice(candidates[0], actualCostRanking)
	priceClass := candidateOrderingPriceClass(candidates[0], actualCostRanking)
	minPrice, maxPrice := firstPrice, firstPrice
	minTTFT, maxTTFT := int64(0), int64(0)
	for _, candidate := range candidates {
		candidatePrice, comparable := candidateOrderingPrice(candidate, actualCostRanking)
		if !comparable || candidateOrderingPriceClass(candidate, actualCostRanking) != priceClass {
			priceComparable = false
		}
		if candidatePrice < minPrice {
			minPrice = candidatePrice
		}
		if candidatePrice > maxPrice {
			maxPrice = candidatePrice
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
			candidatePrice, _ := candidateOrderingPrice(candidates[index], actualCostRanking)
			priceScore = inverseRangeScore(candidatePrice, minPrice, maxPrice)
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

func assignCustomScores(candidates []Candidate, weights CustomWeights, actualCostRanking bool) {
	if len(candidates) == 0 {
		return
	}
	firstPrice, priceComparable := candidateOrderingPrice(candidates[0], actualCostRanking)
	priceClass := candidateOrderingPriceClass(candidates[0], actualCostRanking)
	minPrice, maxPrice := firstPrice, firstPrice
	minCache, maxCache := int64(0), int64(0)
	cacheKnown := false
	minTTFT, maxTTFT := int64(0), int64(0)
	for _, candidate := range candidates {
		candidatePrice, comparable := candidateOrderingPrice(candidate, actualCostRanking)
		if !comparable || candidateOrderingPriceClass(candidate, actualCostRanking) != priceClass {
			priceComparable = false
		}
		if candidatePrice < minPrice {
			minPrice = candidatePrice
		}
		if candidatePrice > maxPrice {
			maxPrice = candidatePrice
		}
		if candidate.RoutingCacheWeightKnown {
			if !cacheKnown || candidate.RoutingCacheWeightPPM < minCache {
				minCache = candidate.RoutingCacheWeightPPM
			}
			if !cacheKnown || candidate.RoutingCacheWeightPPM > maxCache {
				maxCache = candidate.RoutingCacheWeightPPM
			}
			cacheKnown = true
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
		costScore := int64(500_000)
		if priceComparable {
			candidatePrice, _ := candidateOrderingPrice(candidates[index], actualCostRanking)
			costScore = inverseRangeScore(candidatePrice, minPrice, maxPrice)
		}
		cacheScore := int64(500_000)
		if candidates[index].RoutingCacheWeightKnown {
			cacheScore = directRangeScore(candidates[index].RoutingCacheWeightPPM, minCache, maxCache)
		}
		ttftScore := int64(500_000)
		if candidates[index].TTFTKnowledge == TTFTKnown {
			ttftScore = inverseRangeScore(candidates[index].TTFTP95MS, minTTFT, maxTTFT)
		}
		candidates[index].CustomScorePPM =
			(cacheScore*int64(weights.Cache) + costScore*int64(weights.Cost) +
				ttftScore*int64(weights.TTFT) + candidates[index].StabilityPPM*int64(weights.Stability)) / 100
	}
}

func candidateOrderingPrice(candidate Candidate, actualCostRanking bool) (int64, bool) {
	price := candidateRoutePrice(candidate)
	if actualCostRanking {
		if candidateHasActualInputCost(candidate) {
			return candidate.ActualInputCostPPM, true
		}
		// Static route price remains an operational tie-breaker only when the
		// exact two-price prediction is unavailable. It is never exposed as a
		// predicted input cost or compared against a measured formula result.
		return price.ScorePPM, price.StaticComparable
	}
	return price.ScorePPM, price.StaticComparable
}

func candidateOrderingPriceClass(candidate Candidate, actualCostRanking bool) string {
	price := candidateRoutePrice(candidate)
	if actualCostRanking {
		if candidateHasActualInputCost(candidate) {
			return price.ActualInputComparisonClass
		}
		return ""
	}
	return price.ComparisonClass
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

func directRangeScore(value, minimum, maximum int64) int64 {
	if maximum <= minimum {
		return 1_000_000
	}
	if value <= minimum {
		return 0
	}
	if value >= maximum {
		return 1_000_000
	}
	return (value - minimum) * 1_000_000 / (maximum - minimum)
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
