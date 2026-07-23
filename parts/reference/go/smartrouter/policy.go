package smartrouter

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	PolicyVersion       = 4
	LegacyPolicyVersion = 3
)

const (
	DefaultPolicyMaxAttempts      = 3
	DefaultPolicyFailureThreshold = 3
)

type Strategy string

const (
	StrategyPrice     Strategy = "price"
	StrategyStability Strategy = "stability"
	StrategyLatency   Strategy = "latency"
	StrategyBalanced  Strategy = "balanced"
	StrategyManual    Strategy = "manual"
)

type OrderMode string

const (
	OrderAutoPrice OrderMode = "auto_price"
	OrderManual    OrderMode = "manual"
)

type RecoveryProfile string

const (
	RecoveryFast     RecoveryProfile = "fast"
	RecoveryBalanced RecoveryProfile = "balanced"
	RecoveryStable   RecoveryProfile = "stable"
)

type TTFTMetric string

const TTFTMetricP95 TTFTMetric = "p95"

type QueueMode string

const (
	QueueModeNoQueue   QueueMode = "no_queue"
	QueueModeShortWait QueueMode = "short_wait"
)

type AffinityMode string

const (
	AffinityModeFixedPremium      AffinityMode = "fixed_premium"
	AffinityModeEconomicBreakEven AffinityMode = "economic_break_even"
)

type ModelCompressionThreshold struct {
	Model  string `json:"model"`
	Tokens int    `json:"tokens"`
}

// Policy is the versioned JSON contract stored on an intelligent-routing key.
// Normalization never supplies a price limit or adds a pool, group, or route.
type Policy struct {
	Version    int      `json:"version"`
	PolicyID   string   `json:"policy_id"`
	Enabled    bool     `json:"enabled"`
	Strategy   Strategy `json:"strategy,omitempty"`
	PoolPolicy string   `json:"pool_policy"`
	// OrderMode is retained for v3 policy compatibility. New policies use
	// Strategy; a missing Strategy is conservatively derived from OrderMode.
	OrderMode                  OrderMode                   `json:"order_mode"`
	ManualGroupOrder           []string                    `json:"manual_group_order"`
	MaxAttempts                int                         `json:"max_attempts,omitempty"`
	FailureThreshold           int                         `json:"failure_threshold,omitempty"`
	MaxEffectiveRatioPPM       int64                       `json:"max_effective_ratio_ppm"`
	ExcludedGroups             []string                    `json:"excluded_groups"`
	ExcludedRoutes             []string                    `json:"excluded_routes"`
	ContractOverrides          map[string]ContractOverride `json:"contract_overrides"`
	HealthGuard                bool                        `json:"health_guard"`
	RecoveryProfile            RecoveryProfile             `json:"recovery_profile"`
	TTFTPolicy                 TTFTPolicy                  `json:"ttft_policy"`
	QueuePolicy                QueuePolicy                 `json:"queue_policy"`
	BalancedWeights            BalancedWeights             `json:"balanced_weights,omitempty"`
	AffinityEnabled            bool                        `json:"affinity_enabled,omitempty"`
	AffinityMode               AffinityMode                `json:"affinity_mode,omitempty"`
	AffinityMaxPremiumPercent  int                         `json:"affinity_max_premium_percent,omitempty"`
	AffinityTTLSeconds         int                         `json:"affinity_ttl_seconds,omitempty"`
	CompressionThresholds      []ModelCompressionThreshold `json:"compression_thresholds,omitempty"`
	AllowFutureGroups          bool                        `json:"allow_future_groups,omitempty"`
	AllowFutureCertifiedRoutes bool                        `json:"allow_future_certified_routes"`
}

type ContractOverride struct {
	PoolPolicy       string    `json:"pool_policy,omitempty"`
	OrderMode        OrderMode `json:"order_mode,omitempty"`
	ManualRouteOrder []string  `json:"manual_route_order,omitempty"`
}

type TTFTPolicy struct {
	Enabled    bool       `json:"enabled"`
	Metric     TTFTMetric `json:"metric"`
	TargetMS   int64      `json:"target_ms"`
	HardMaxMS  int64      `json:"hard_max_ms"`
	MinSamples int        `json:"min_samples"`
}

type QueuePolicy struct {
	Mode             QueueMode `json:"mode"`
	MaxWaitMS        int64     `json:"max_wait_ms"`
	MinSavingPercent int       `json:"min_saving_percent"`
}

type BalancedWeights struct {
	Stability int `json:"stability"`
	Price     int `json:"price"`
	TTFT      int `json:"ttft"`
}

func DefaultBalancedWeights() BalancedWeights {
	return BalancedWeights{Stability: 45, Price: 40, TTFT: 15}
}

// NormalizePolicy returns a canonical deep copy suitable for hashing or
// persistence. It only applies the fail-closed no-queue default.
func NormalizePolicy(policy Policy) Policy {
	if policy.Strategy == "" {
		policy.Strategy = strategyFromOrderMode(policy.OrderMode)
	}
	if policy.OrderMode == "" {
		policy.OrderMode = orderModeFromStrategy(policy.Strategy)
	}
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = DefaultPolicyMaxAttempts
	}
	if policy.FailureThreshold == 0 {
		policy.FailureThreshold = DefaultPolicyFailureThreshold
	}
	if policy.BalancedWeights == (BalancedWeights{}) {
		policy.BalancedWeights = DefaultBalancedWeights()
	}
	if policy.RecoveryProfile == "" {
		policy.RecoveryProfile = RecoveryBalanced
	}
	if policy.Version == LegacyPolicyVersion {
		policy.AllowFutureGroups = policy.AllowFutureCertifiedRoutes
	}
	if policy.Version == PolicyVersion {
		// The v4 group-level flag is authoritative. Keep the legacy alias in
		// sync so older rolling-upgrade nodes cannot widen a fixed Key scope.
		policy.AllowFutureCertifiedRoutes = policy.AllowFutureGroups
	}
	if policy.Version == LegacyPolicyVersion || (!policy.AffinityEnabled && policy.AffinityMaxPremiumPercent == 0 && policy.AffinityTTLSeconds == 0) {
		policy.AffinityEnabled = true
	}
	if policy.Version == LegacyPolicyVersion && policy.AffinityMaxPremiumPercent == 0 {
		policy.AffinityMaxPremiumPercent = 10
	}
	if policy.AffinityTTLSeconds == 0 {
		policy.AffinityTTLSeconds = 1800
	}
	policy.ExcludedGroups = canonicalSet(policy.ExcludedGroups)
	policy.ExcludedRoutes = canonicalSet(policy.ExcludedRoutes)
	policy.ManualGroupOrder = canonicalOrder(policy.ManualGroupOrder)
	policy.CompressionThresholds = canonicalCompressionThresholds(policy.CompressionThresholds)
	if policy.QueuePolicy.Mode == "" {
		policy.QueuePolicy.Mode = QueueModeNoQueue
	}
	if policy.ContractOverrides == nil {
		policy.ContractOverrides = map[string]ContractOverride{}
	} else {
		overrides := make(map[string]ContractOverride, len(policy.ContractOverrides))
		for contractID, override := range policy.ContractOverrides {
			override.ManualRouteOrder = append([]string(nil), override.ManualRouteOrder...)
			overrides[contractID] = override
		}
		policy.ContractOverrides = overrides
	}
	return policy
}

func canonicalCompressionThresholds(values []ModelCompressionThreshold) []ModelCompressionThreshold {
	if len(values) == 0 {
		return []ModelCompressionThreshold{}
	}
	result := append([]ModelCompressionThreshold(nil), values...)
	for index := range result {
		result[index].Model = strings.TrimSpace(result[index].Model)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Model < result[j].Model
	})
	return result
}

func canonicalOrder(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			result = append(result, value)
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func canonicalSet(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (policy Policy) Validate() error {
	if policy.Version != PolicyVersion && policy.Version != LegacyPolicyVersion {
		return fmt.Errorf("smartrouter: policy version must be %d or legacy %d", PolicyVersion, LegacyPolicyVersion)
	}
	if policy.Version == LegacyPolicyVersion && policy.PolicyID == "" {
		return errors.New("smartrouter: policy_id is required")
	}
	if policy.Version == LegacyPolicyVersion && policy.PoolPolicy == "" {
		return errors.New("smartrouter: pool_policy is required")
	}
	strategy := policy.EffectiveStrategy()
	if !validStrategy(strategy) {
		return fmt.Errorf("smartrouter: unsupported strategy %q", strategy)
	}
	if policy.OrderMode != "" && !validOrderMode(policy.OrderMode) {
		return fmt.Errorf("smartrouter: unsupported order_mode %q", policy.OrderMode)
	}
	if policy.MaxEffectiveRatioPPM < 0 || (policy.Version == LegacyPolicyVersion && policy.MaxEffectiveRatioPPM == 0) {
		return errors.New("smartrouter: max_effective_ratio_ppm must be non-negative; legacy v3 requires a positive value")
	}
	if !validRecoveryProfile(policy.RecoveryProfile) {
		return fmt.Errorf("smartrouter: unsupported recovery_profile %q", policy.RecoveryProfile)
	}
	if policy.MaxAttempts < 0 || policy.MaxAttempts > 8 {
		return errors.New("smartrouter: max_attempts must be between 1 and 8 when specified")
	}
	if policy.FailureThreshold < 0 || policy.FailureThreshold > 100 {
		return errors.New("smartrouter: failure_threshold must be positive when specified")
	}
	if err := policy.BalancedWeights.validate(strategy); err != nil {
		return err
	}
	if policy.AffinityMaxPremiumPercent < 0 || policy.AffinityMaxPremiumPercent > 1000 || policy.AffinityTTLSeconds < 0 {
		return errors.New("smartrouter: affinity limits are out of range")
	}
	if policy.AffinityMode != "" && policy.AffinityMode != AffinityModeFixedPremium &&
		policy.AffinityMode != AffinityModeEconomicBreakEven {
		return fmt.Errorf("smartrouter: unsupported affinity_mode %q", policy.AffinityMode)
	}
	seenCompressionModels := make(map[string]struct{}, len(policy.CompressionThresholds))
	for _, threshold := range policy.CompressionThresholds {
		if threshold.Model == "" {
			return errors.New("smartrouter: compression_thresholds cannot contain an empty model")
		}
		if threshold.Tokens <= 0 || threshold.Tokens > 100_000_000 {
			return fmt.Errorf("smartrouter: compression threshold for model %q is out of range", threshold.Model)
		}
		if _, exists := seenCompressionModels[threshold.Model]; exists {
			return fmt.Errorf("smartrouter: compression_thresholds repeats model %q", threshold.Model)
		}
		seenCompressionModels[threshold.Model] = struct{}{}
	}
	for _, group := range policy.ExcludedGroups {
		if group == "" {
			return errors.New("smartrouter: excluded_groups cannot contain an empty group")
		}
	}
	for _, routeID := range policy.ExcludedRoutes {
		if routeID == "" {
			return errors.New("smartrouter: excluded_routes cannot contain an empty route")
		}
	}
	seenManualGroups := make(map[string]struct{}, len(policy.ManualGroupOrder))
	for _, group := range policy.ManualGroupOrder {
		if strings.TrimSpace(group) == "" {
			return errors.New("smartrouter: manual_group_order cannot contain an empty group")
		}
		if _, exists := seenManualGroups[group]; exists {
			return fmt.Errorf("smartrouter: manual_group_order repeats group %q", group)
		}
		seenManualGroups[group] = struct{}{}
	}
	if policy.Version == PolicyVersion && !policy.AllowFutureGroups && len(policy.ManualGroupOrder) == 0 {
		return errors.New("smartrouter: a fixed v4 group scope requires at least one manual_group_order entry")
	}
	for contractID, override := range policy.ContractOverrides {
		if contractID == "" {
			return errors.New("smartrouter: contract_overrides cannot contain an empty contract ID")
		}
		if override.OrderMode != "" && !validOrderMode(override.OrderMode) {
			return fmt.Errorf("smartrouter: contract %q has unsupported order_mode %q", contractID, override.OrderMode)
		}
		resolvedOrder := override.OrderMode
		if resolvedOrder == "" {
			resolvedOrder = orderModeFromStrategy(strategy)
		}
		if len(override.ManualRouteOrder) > 0 && resolvedOrder != OrderManual {
			return fmt.Errorf("smartrouter: contract %q supplies manual_route_order without manual ordering", contractID)
		}
		seenRoutes := make(map[string]struct{}, len(override.ManualRouteOrder))
		for _, routeID := range override.ManualRouteOrder {
			if routeID == "" {
				return fmt.Errorf("smartrouter: contract %q has an empty manual route", contractID)
			}
			if _, ok := seenRoutes[routeID]; ok {
				return fmt.Errorf("smartrouter: contract %q repeats manual route %q", contractID, routeID)
			}
			seenRoutes[routeID] = struct{}{}
		}
	}
	if err := policy.TTFTPolicy.validate(); err != nil {
		return err
	}
	return policy.QueuePolicy.validate()
}

// AllowsGroup applies the v4 per-Key group scope independently of ordering.
// ManualGroupOrder is both the selected-group set and, for manual strategy,
// its order. Legacy v3 policies retain their route-level compatibility rules.
func (policy Policy) AllowsGroup(group string) bool {
	if policy.Version != PolicyVersion || policy.AllowFutureGroups {
		return true
	}
	for _, allowed := range policy.ManualGroupOrder {
		if allowed == group {
			return true
		}
	}
	return false
}

// EffectiveStrategy maps the v3 order_mode contract into the v4 strategy
// vocabulary without mutating persisted policy JSON.
func (policy Policy) EffectiveStrategy() Strategy {
	if validStrategy(policy.Strategy) {
		return policy.Strategy
	}
	return strategyFromOrderMode(policy.OrderMode)
}

func (policy Policy) EffectiveMaxAttempts() int {
	if policy.MaxAttempts > 0 {
		return policy.MaxAttempts
	}
	return DefaultPolicyMaxAttempts
}

func (policy Policy) EffectiveFailureThreshold() int {
	if policy.FailureThreshold > 0 {
		return policy.FailureThreshold
	}
	return DefaultPolicyFailureThreshold
}

// EffectiveAffinityMode preserves every policy written before V4.7. A missing
// field is the historical fixed-premium contract, while new keys explicitly
// opt into the economic break-even mode.
func (policy Policy) EffectiveAffinityMode() AffinityMode {
	if policy.AffinityMode == AffinityModeEconomicBreakEven {
		return AffinityModeEconomicBreakEven
	}
	return AffinityModeFixedPremium
}

func (policy Policy) CompressionThresholdForModel(model string) (int, bool) {
	model = strings.TrimSpace(model)
	for _, threshold := range policy.CompressionThresholds {
		if threshold.Model == model {
			return threshold.Tokens, true
		}
	}
	return 0, false
}

func (policy Policy) EffectiveBalancedWeights() BalancedWeights {
	if policy.BalancedWeights == (BalancedWeights{}) {
		return DefaultBalancedWeights()
	}
	return policy.BalancedWeights
}

func strategyFromOrderMode(mode OrderMode) Strategy {
	if mode == OrderManual {
		return StrategyManual
	}
	return StrategyPrice
}

func orderModeFromStrategy(strategy Strategy) OrderMode {
	if strategy == StrategyManual {
		return OrderManual
	}
	return OrderAutoPrice
}

func validStrategy(strategy Strategy) bool {
	switch strategy {
	case StrategyPrice, StrategyStability, StrategyLatency, StrategyBalanced, StrategyManual:
		return true
	default:
		return false
	}
}

func validOrderMode(mode OrderMode) bool {
	return mode == OrderAutoPrice || mode == OrderManual
}

func (weights BalancedWeights) validate(strategy Strategy) error {
	if weights == (BalancedWeights{}) && strategy != StrategyBalanced {
		return nil
	}
	if weights.Stability < 0 || weights.Price < 0 || weights.TTFT < 0 {
		return errors.New("smartrouter: balanced weights cannot be negative")
	}
	if weights.Stability+weights.Price+weights.TTFT != 100 {
		return errors.New("smartrouter: balanced weights must total 100")
	}
	return nil
}

func validRecoveryProfile(profile RecoveryProfile) bool {
	return profile == RecoveryFast || profile == RecoveryBalanced || profile == RecoveryStable
}

func (policy TTFTPolicy) validate() error {
	if !policy.Enabled {
		if policy.Metric != "" && policy.Metric != TTFTMetricP95 {
			return fmt.Errorf("smartrouter: unsupported TTFT metric %q", policy.Metric)
		}
		return nil
	}
	if policy.Metric != TTFTMetricP95 {
		return fmt.Errorf("smartrouter: enabled TTFT policy requires metric %q", TTFTMetricP95)
	}
	if policy.TargetMS <= 0 || policy.HardMaxMS <= 0 || policy.TargetMS > policy.HardMaxMS {
		return errors.New("smartrouter: TTFT target_ms and hard_max_ms must be positive and target_ms must not exceed hard_max_ms")
	}
	if policy.MinSamples <= 0 {
		return errors.New("smartrouter: enabled TTFT policy requires positive min_samples")
	}
	return nil
}

func (policy QueuePolicy) validate() error {
	if policy.MaxWaitMS < 0 || policy.MinSavingPercent < 0 || policy.MinSavingPercent > 100 {
		return errors.New("smartrouter: queue limits are out of range")
	}
	switch policy.Mode {
	case QueueModeNoQueue:
		return nil
	case QueueModeShortWait:
		if policy.MaxWaitMS <= 0 {
			return errors.New("smartrouter: short_wait queue requires positive max_wait_ms")
		}
		return nil
	default:
		return fmt.Errorf("smartrouter: unsupported queue mode %q", policy.Mode)
	}
}
