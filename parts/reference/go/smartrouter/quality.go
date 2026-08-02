package smartrouter

import (
	"errors"
	"fmt"
	"math"
)

type ReplayClass uint8

const (
	// ReplayUnsupported is deliberately the zero value so omitted
	// classification fails closed.
	ReplayUnsupported ReplayClass = iota
	ReplaySafeText
	// ReplaySafeImage is a media submission that may move to another route
	// only when the current attempt is known not to have been accepted. A
	// transport failure or a malformed successful response is ambiguous and
	// must stay bound to the attempted route for reconciliation.
	ReplaySafeImage
	// ReplaySafeVideo has the same conservative submission boundary as image
	// generation and additionally creates an asynchronous, route-bound task.
	ReplaySafeVideo
	// ReplaySafeAudio covers speech generation, transcription, and translation.
	// It may change routes only after a concrete rejection proves that the
	// upstream did not accept or complete the media operation.
	ReplaySafeAudio
	ReplayStateBound
	ReplaySideEffecting
)

func (class ReplayClass) String() string {
	switch class {
	case ReplaySafeText:
		return "safe_text"
	case ReplaySafeImage:
		return "safe_image"
	case ReplaySafeVideo:
		return "safe_video"
	case ReplaySafeAudio:
		return "safe_audio"
	case ReplayStateBound:
		return "state_bound"
	case ReplaySideEffecting:
		return "side_effecting"
	default:
		return "unsupported"
	}
}

// IsRoutableReplayClass reports whether the planner may select an initial
// route for this request. It does not grant permission to replay an ambiguous
// media submission; that decision is made after each concrete attempt.
func IsRoutableReplayClass(class ReplayClass) bool {
	switch class {
	case ReplaySafeText, ReplaySafeImage, ReplaySafeVideo, ReplaySafeAudio, ReplaySideEffecting:
		return true
	default:
		return false
	}
}

// IsSyntheticProbeSafe deliberately excludes generation endpoints. Probing a
// text endpoint is read-like and cheap; probing image/video creates billable
// media and needs a separate explicitly-budgeted probe implementation.
func IsSyntheticProbeSafe(class ReplayClass) bool {
	return class == ReplaySafeText
}

type QualityPhase string

const (
	QualityUnknown     QualityPhase = "unknown"
	QualityBootstrap   QualityPhase = "bootstrap"
	QualityHealthy     QualityPhase = "healthy"
	QualityDegraded    QualityPhase = "degraded"
	QualityOpen        QualityPhase = "open"
	QualityHalfOpen    QualityPhase = "half_open"
	QualityWarming     QualityPhase = "warming"
	QualityQuarantined QualityPhase = "quarantined"
)

type QualitySnapshot struct {
	Version string                  `json:"version"`
	Routes  map[string]QualityState `json:"routes"`
}

type QualityState struct {
	Phase                   QualityPhase   `json:"phase"`
	Revision                uint64         `json:"revision"`
	Epoch                   uint64         `json:"epoch"`
	ConsecutiveSuccesses    int            `json:"consecutive_successes"`
	ConsecutiveHardFailures int            `json:"consecutive_hard_failures"`
	TotalSuccesses          uint64         `json:"total_successes"`
	TotalHardFailures       uint64         `json:"total_hard_failures"`
	RealSuccesses           uint64         `json:"real_successes"`
	RealHardFailures        uint64         `json:"real_hard_failures"`
	TotalProbeFailures      uint64         `json:"total_probe_failures"`
	ReliabilityPPM          int64          `json:"reliability_ppm"`
	ReliabilitySamples      uint64         `json:"reliability_samples"`
	ProbeFailures           int            `json:"probe_failures"`
	SuccessfulWarmSamples   int            `json:"successful_warm_samples"`
	WarmingTrafficPPM       int64          `json:"warming_traffic_ppm"`
	WarmingEpoch            uint64         `json:"warming_epoch"`
	OpenCount               int            `json:"open_count"`
	LastUpdatedMS           int64          `json:"last_updated_ms"`
	FirstFailureMS          int64          `json:"first_failure_ms"`
	LastFailureMS           int64          `json:"last_failure_ms"`
	FailureWindowUntilMS    int64          `json:"failure_window_until_ms"`
	LastSuccessMS           int64          `json:"last_success_ms"`
	LastRealSuccessMS       int64          `json:"last_real_success_ms"`
	LastRealFailureMS       int64          `json:"last_real_failure_ms"`
	LastRealOutcome         QualityOutcome `json:"last_real_outcome"`
	LastOutcome             QualityOutcome `json:"last_outcome"`
	LastHTTPStatus          int            `json:"last_http_status,omitempty"`
	LastErrorSummary        string         `json:"last_error_summary,omitempty"`
	// Probe evidence is kept separately from ordinary traffic evidence so the
	// health UI can distinguish a real request from a background recovery check.
	LastProbeAtMS         int64          `json:"last_probe_at_ms"`
	LastProbeOutcome      QualityOutcome `json:"last_probe_outcome,omitempty"`
	LastProbeHTTPStatus   int            `json:"last_probe_http_status,omitempty"`
	LastProbeErrorSummary string         `json:"last_probe_error_summary,omitempty"`
	ProbeSuccesses        uint64         `json:"probe_successes"`
	// RecoveryStartedMS is the beginning of the current continuous outage.
	// Unlike the key-local hard-failure window it is not reset merely because a
	// quiet period elapsed; only a successful reachability observation starts a
	// new recovery episode.
	RecoveryStartedMS    int64 `json:"recovery_started_ms"`
	SlowRecoveryAttempts int   `json:"slow_recovery_attempts"`
	StableSinceMS        int64 `json:"stable_since_ms"`
	NextProbeAtMS        int64 `json:"next_probe_at_ms"`
	SampleDueAtMS        int64 `json:"sample_due_at_ms"`
}

type QualityOutcome string

const (
	OutcomeSuccess         QualityOutcome = "success"
	OutcomeInfrastructure  QualityOutcome = "infrastructure_failure"
	OutcomeCredential      QualityOutcome = "credential_failure"
	OutcomeCapacityLimited QualityOutcome = "capacity_limited"
	OutcomeUserRejected    QualityOutcome = "user_rejected"
	OutcomeContentRejected QualityOutcome = "content_rejected"
	OutcomeClientCancelled QualityOutcome = "client_cancelled"
)

type QualityEvent struct {
	AtMS            int64          `json:"at_ms"`
	Outcome         QualityOutcome `json:"outcome"`
	RetryAfterMS    int64          `json:"retry_after_ms,omitempty"`
	Probe           bool           `json:"probe,omitempty"`
	SyntheticProbe  bool           `json:"synthetic_probe,omitempty"`
	VirginBootstrap bool           `json:"virgin_bootstrap,omitempty"`
	HTTPStatus      int            `json:"http_status,omitempty"`
	ErrorSummary    string         `json:"error_summary,omitempty"`
}

type CredentialSnapshot struct {
	Version string                     `json:"version"`
	Domains map[string]CredentialState `json:"domains"`
}

type CredentialState struct {
	Epoch               uint64 `json:"epoch"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	BlockedUntilMS      int64  `json:"blocked_until_ms"`
	LastFailureMS       int64  `json:"last_failure_ms"`
	LastSuccessMS       int64  `json:"last_success_ms"`
	Revision            uint64 `json:"revision"`
}

type WarmingStage struct {
	SuccessfulSamples int   `json:"successful_samples"`
	TrafficPPM        int64 `json:"traffic_ppm"`
}

type QualityConfig struct {
	HardFailureWindowMS         int64          `json:"hard_failure_window_ms"`
	FastRecoveryIntervalMS      int64          `json:"fast_recovery_interval_ms"`
	FastRecoveryWindowMS        int64          `json:"fast_recovery_window_ms"`
	SlowRecoveryBackoffMS       []int64        `json:"slow_recovery_backoff_ms"`
	OpenBackoffMS               []int64        `json:"open_backoff_ms"`
	CredentialBackoffMS         int64          `json:"credential_backoff_ms"`
	DegradedRecoverySuccesses   int            `json:"degraded_recovery_successes"`
	HealthyWarmSamples          int            `json:"healthy_warm_samples"`
	FastCanaryMinSamples        int            `json:"fast_canary_min_samples"`
	FastCanaryMinReliabilityPPM int64          `json:"fast_canary_min_reliability_ppm"`
	WarmingSampleIntervalMS     int64          `json:"warming_sample_interval_ms"`
	WarmingStages               []WarmingStage `json:"warming_stages"`
}

func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		HardFailureWindowMS:         60_000,
		FastRecoveryIntervalMS:      60_000,
		FastRecoveryWindowMS:        60 * 60_000,
		SlowRecoveryBackoffMS:       []int64{5 * 60_000, 15 * 60_000, 60 * 60_000},
		OpenBackoffMS:               []int64{60_000, 120_000, 240_000, 480_000, 900_000},
		CredentialBackoffMS:         900_000,
		DegradedRecoverySuccesses:   3,
		HealthyWarmSamples:          10,
		FastCanaryMinSamples:        5,
		FastCanaryMinReliabilityPPM: 800_000,
		WarmingSampleIntervalMS:     30_000,
		WarmingStages: []WarmingStage{
			{SuccessfulSamples: 1, TrafficPPM: 100_000},
			{SuccessfulSamples: 3, TrafficPPM: 250_000},
			{SuccessfulSamples: 6, TrafficPPM: 500_000},
		},
	}
}

func (config QualityConfig) Validate() error {
	if config.HardFailureWindowMS <= 0 || config.FastRecoveryIntervalMS <= 0 ||
		config.FastRecoveryWindowMS < config.FastRecoveryIntervalMS ||
		config.CredentialBackoffMS <= 0 || config.WarmingSampleIntervalMS <= 0 {
		return errors.New("smartrouter: quality durations must be positive")
	}
	if config.DegradedRecoverySuccesses <= 0 || config.HealthyWarmSamples <= 0 ||
		config.FastCanaryMinSamples <= 0 || config.FastCanaryMinReliabilityPPM < 0 ||
		config.FastCanaryMinReliabilityPPM > 1_000_000 {
		return errors.New("smartrouter: quality sample thresholds must be positive")
	}
	if len(config.OpenBackoffMS) == 0 {
		return errors.New("smartrouter: at least one open backoff is required")
	}
	for _, delay := range config.OpenBackoffMS {
		if delay <= 0 {
			return errors.New("smartrouter: open backoffs must be positive")
		}
	}
	if len(config.SlowRecoveryBackoffMS) == 0 {
		return errors.New("smartrouter: at least one slow recovery backoff is required")
	}
	previousDelay := int64(0)
	for _, delay := range config.SlowRecoveryBackoffMS {
		if delay <= 0 || delay < previousDelay {
			return errors.New("smartrouter: slow recovery backoffs must be positive and non-decreasing")
		}
		previousDelay = delay
	}
	previousSamples := 0
	previousTraffic := int64(0)
	for _, stage := range config.WarmingStages {
		if stage.SuccessfulSamples <= previousSamples || stage.TrafficPPM <= previousTraffic || stage.TrafficPPM > 1_000_000 {
			return errors.New("smartrouter: warming stages must increase and stay within one million PPM")
		}
		previousSamples = stage.SuccessfulSamples
		previousTraffic = stage.TrafficPPM
	}
	return nil
}

func InitialQuality(stableFallback bool) QualityState {
	if stableFallback {
		return QualityState{Phase: QualityBootstrap, Epoch: 1, ReliabilityPPM: 500_000}
	}
	return QualityState{Phase: QualityUnknown, Epoch: 1, ReliabilityPPM: 500_000}
}

func IsVirginUnknown(state QualityState) bool {
	return state.Phase == QualityUnknown && state.Epoch <= 1 && state.Revision == 0 &&
		state.ConsecutiveSuccesses == 0 && state.ConsecutiveHardFailures == 0 &&
		state.TotalSuccesses == 0 && state.TotalHardFailures == 0 &&
		state.RealSuccesses == 0 && state.RealHardFailures == 0 &&
		state.ProbeSuccesses == 0 && state.ProbeFailures == 0 && state.TotalProbeFailures == 0 &&
		state.ReliabilitySamples == 0 && state.SuccessfulWarmSamples == 0 &&
		state.WarmingEpoch == 0 && state.OpenCount == 0 &&
		state.LastUpdatedMS == 0 && state.FirstFailureMS == 0 && state.LastFailureMS == 0 &&
		state.FailureWindowUntilMS == 0 && state.LastSuccessMS == 0 &&
		state.LastRealSuccessMS == 0 && state.LastRealFailureMS == 0 &&
		state.LastProbeAtMS == 0 && state.StableSinceMS == 0 &&
		state.RecoveryStartedMS == 0 && state.SlowRecoveryAttempts == 0 &&
		state.NextProbeAtMS == 0 && state.SampleDueAtMS == 0
}

// BeginProbe is a pure compare-and-transition helper. A runtime store must
// still apply it with compare-and-swap to guarantee a single HalfOpen owner.
func BeginProbe(state QualityState, nowMS int64) (QualityState, bool) {
	if state.Phase == "" {
		state.Phase = QualityUnknown
	}
	state, _ = NormalizeAutomaticRecoveryState(state, nowMS)
	if state.Phase != QualityUnknown && state.Phase != QualityOpen && state.Phase != QualityDegraded {
		return state, false
	}
	if state.NextProbeAtMS > nowMS {
		return state, false
	}
	state.Phase = QualityHalfOpen
	state.Revision++
	state.LastUpdatedMS = nowMS
	return state, true
}

// ReduceQuality applies one already-classified attempt outcome. Capacity,
// user, content, and cancellation outcomes intentionally leave health intact.
func ReduceQuality(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	if state.Phase == "" {
		state.Phase = QualityUnknown
	}
	if event.AtMS < state.LastUpdatedMS {
		event.AtMS = state.LastUpdatedMS
	}
	if event.Probe {
		state.LastProbeAtMS = event.AtMS
		state.LastProbeOutcome = event.Outcome
		state.LastProbeHTTPStatus = event.HTTPStatus
		state.LastProbeErrorSummary = event.ErrorSummary
		if event.Outcome == OutcomeSuccess {
			state.ProbeSuccesses++
		}
	}
	// Request reliability and synthetic recovery evidence are independent. A
	// probe can prove current reachability, but it is not a user request and must
	// never improve or reduce the SLA/reliability estimate used by policies.
	switch event.Outcome {
	case OutcomeSuccess:
		if !event.Probe {
			state.RealSuccesses++
			state.LastRealSuccessMS = event.AtMS
			state.LastRealOutcome = event.Outcome
		}
	case OutcomeCredential, OutcomeInfrastructure:
		if event.Probe {
			state.TotalProbeFailures++
		} else {
			state.RealHardFailures++
			state.LastRealFailureMS = event.AtMS
			state.LastRealOutcome = event.Outcome
		}
	}
	switch event.Outcome {
	case OutcomeCapacityLimited:
		state.LastOutcome = event.Outcome
		state.LastHTTPStatus = event.HTTPStatus
		state.LastErrorSummary = event.ErrorSummary
		state.LastUpdatedMS = event.AtMS
		state.Revision++
		return state
	case OutcomeUserRejected, OutcomeContentRejected, OutcomeClientCancelled:
		return state
	case OutcomeSuccess:
		return reduceQualitySuccess(state, event, config)
	case OutcomeCredential:
		state = recordHardFailureEvidence(state, event, config)
		return openQuality(state, event.AtMS, max64(config.CredentialBackoffMS, event.RetryAfterMS), config)
	case OutcomeInfrastructure:
		if event.Probe {
			state = recordHardFailureEvidence(state, event, config)
			return openQuality(state, event.AtMS, event.RetryAfterMS, config)
		}
		previousPhase := state.Phase
		state = recordHardFailureEvidence(state, event, config)
		if previousPhase == QualityHealthy {
			if state.ConsecutiveHardFailures >= 2 {
				return openQuality(state, event.AtMS, event.RetryAfterMS, config)
			}
			state.Phase = QualityDegraded
			state.ConsecutiveSuccesses = 0
			state.LastUpdatedMS = event.AtMS
			state.SampleDueAtMS = saturatingAdd(event.AtMS, config.WarmingSampleIntervalMS)
			state.Revision++
			return state
		}
		return openQuality(state, event.AtMS, event.RetryAfterMS, config)
	default:
		return state
	}
}

func recordHardFailureEvidence(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	if state.RecoveryStartedMS == 0 {
		state.RecoveryStartedMS = event.AtMS
	}
	if state.FirstFailureMS == 0 {
		state.FirstFailureMS = event.AtMS
	}
	if event.Probe {
		state.ProbeFailures++
		state.LastFailureMS = event.AtMS
		state.LastOutcome = event.Outcome
		state.LastHTTPStatus = event.HTTPStatus
		state.LastErrorSummary = event.ErrorSummary
		return state
	}
	withinWindow := state.LastFailureMS > 0 && event.AtMS-state.LastFailureMS <= config.HardFailureWindowMS
	if !withinWindow {
		state.ConsecutiveHardFailures = 1
	} else {
		state.ConsecutiveHardFailures++
	}
	state.TotalHardFailures++
	state.ReliabilityPPM = updateReliabilityPPM(state.ReliabilityPPM, false, state.ReliabilitySamples)
	state.ReliabilitySamples++
	state.ConsecutiveSuccesses = 0
	state.LastFailureMS = event.AtMS
	state.FailureWindowUntilMS = saturatingAdd(event.AtMS, config.HardFailureWindowMS)
	state.LastOutcome = event.Outcome
	state.LastHTTPStatus = event.HTTPStatus
	state.LastErrorSummary = event.ErrorSummary
	return state
}

func reduceQualitySuccess(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	atMS := event.AtMS
	state.Revision++
	state.LastUpdatedMS = atMS
	state.ProbeFailures = 0
	// A successful upstream completion starts a fresh outage episode. Keeping
	// the historical open counter would make an intermittently healthy route
	// inherit the maximum backoff forever, even after it has produced valid
	// responses again. Warming/canary admission still prevents an immediate
	// full-traffic return, so resetting the retry tier here does not remove the
	// recovery hysteresis.
	state.OpenCount = 0
	state.SlowRecoveryAttempts = 0
	state.NextProbeAtMS = 0
	state.LastSuccessMS = atMS
	state.LastOutcome = OutcomeSuccess
	state.LastHTTPStatus = 0
	state.LastErrorSummary = ""
	if event.Probe {
		state.ConsecutiveSuccesses = 0
	} else {
		state.ConsecutiveHardFailures = 0
		state.FirstFailureMS = 0
		state.RecoveryStartedMS = 0
		state.FailureWindowUntilMS = 0
		state.TotalSuccesses++
		state.ReliabilityPPM = updateReliabilityPPM(state.ReliabilityPPM, true, state.ReliabilitySamples)
		state.ReliabilitySamples++
		state.ConsecutiveSuccesses++
	}
	switch state.Phase {
	case QualityBootstrap:
		// A legacy bootstrap route starts here so one request can validate it.
		// Its first success is cold-start evidence, not recovery from a known
		// failure, so it remains available to ordinary requests.
		state.Phase = QualityHealthy
		state.ConsecutiveSuccesses = 1
		state.SuccessfulWarmSamples = 0
		state.WarmingTrafficPPM = 1_000_000
		state.StableSinceMS = atMS
		state.NextProbeAtMS = 0
		state.SampleDueAtMS = 0
	case QualityUnknown:
		// A virgin automatically discovered route has no failure evidence yet.
		// Its first successful real request establishes normal availability;
		// warming is reserved for routes that are recovering from an outage.
		if !event.Probe && state.TotalHardFailures == 0 && state.LastFailureMS == 0 {
			state.Phase = QualityHealthy
			state.ConsecutiveSuccesses = 1
			state.SuccessfulWarmSamples = 0
			state.WarmingTrafficPPM = 1_000_000
			state.StableSinceMS = atMS
			state.NextProbeAtMS = 0
			state.SampleDueAtMS = 0
			break
		}
		fallthrough
	case QualityHalfOpen, QualityOpen:
		if event.VirginBootstrap {
			state.Phase = QualityHealthy
			state.ConsecutiveSuccesses = 1
			state.SuccessfulWarmSamples = 0
			state.WarmingTrafficPPM = 1_000_000
			state.StableSinceMS = atMS
			state.NextProbeAtMS = 0
			state.SampleDueAtMS = 0
			break
		}
		state.Phase = QualityWarming
		state.ConsecutiveSuccesses = 0
		state.SuccessfulWarmSamples = 0
		if !event.Probe {
			state.ConsecutiveSuccesses = 1
			state.SuccessfulWarmSamples = 1
		}
		state.WarmingEpoch++
		state.StableSinceMS = atMS
		state.WarmingTrafficPPM = warmingTraffic(1, config.WarmingStages)
		state.SampleDueAtMS = 0
	case QualityWarming:
		if event.Probe {
			state.SampleDueAtMS = 0
			break
		}
		state.SuccessfulWarmSamples++
		fastCanaryEligible := config.FastCanaryMinSamples > 0 &&
			state.ReliabilitySamples >= uint64(config.FastCanaryMinSamples) &&
			state.ReliabilityPPM >= config.FastCanaryMinReliabilityPPM
		if state.SuccessfulWarmSamples >= config.HealthyWarmSamples ||
			(fastCanaryEligible && state.SuccessfulWarmSamples >= 1) {
			state.Phase = QualityHealthy
			state.WarmingTrafficPPM = 1_000_000
			state.SampleDueAtMS = 0
		} else {
			state.WarmingTrafficPPM = warmingTraffic(state.SuccessfulWarmSamples, config.WarmingStages)
			state.SampleDueAtMS = saturatingAdd(atMS, config.WarmingSampleIntervalMS)
		}
	case QualityDegraded:
		if event.Probe && event.SyntheticProbe {
			state.Phase = QualityWarming
			state.ConsecutiveSuccesses = 0
			state.SuccessfulWarmSamples = 0
			state.WarmingEpoch++
			state.StableSinceMS = atMS
			state.WarmingTrafficPPM = warmingTraffic(1, config.WarmingStages)
			state.SampleDueAtMS = 0
			break
		}
		if state.ConsecutiveSuccesses >= config.DegradedRecoverySuccesses {
			state.Phase = QualityHealthy
			state.StableSinceMS = atMS
			state.SampleDueAtMS = 0
		} else {
			state.SampleDueAtMS = saturatingAdd(atMS, config.WarmingSampleIntervalMS)
		}
	case QualityHealthy:
		if state.StableSinceMS == 0 {
			state.StableSinceMS = atMS
		}
	}
	return state
}

func openQuality(state QualityState, atMS, retryAfterMS int64, config QualityConfig) QualityState {
	delayMS := config.FastRecoveryIntervalMS
	if state.RecoveryStartedMS > 0 && atMS-state.RecoveryStartedMS >= config.FastRecoveryWindowMS {
		index := state.SlowRecoveryAttempts
		if index >= len(config.SlowRecoveryBackoffMS) {
			index = len(config.SlowRecoveryBackoffMS) - 1
		}
		delayMS = config.SlowRecoveryBackoffMS[index]
		state.SlowRecoveryAttempts++
	} else {
		state.SlowRecoveryAttempts = 0
	}
	delayMS = max64(delayMS, retryAfterMS)
	state.Phase = QualityOpen
	state.Revision++
	state.OpenCount++
	state.ConsecutiveSuccesses = 0
	state.WarmingTrafficPPM = 0
	state.SuccessfulWarmSamples = 0
	state.LastFailureMS = atMS
	state.LastUpdatedMS = atMS
	state.NextProbeAtMS = saturatingAdd(atMS, delayMS)
	state.SampleDueAtMS = 0
	return state
}

func updateReliabilityPPM(previous int64, success bool, samples uint64) int64 {
	if samples == 0 || previous < 0 || previous > 1_000_000 {
		previous = 500_000
	}
	target := int64(0)
	if success {
		target = 1_000_000
	}
	// A fixed-point EWMA keeps state bounded while remaining deterministic.
	return (previous*9 + target) / 10
}

// KeySuppressed derives one key's soft circuit breaker from shared evidence.
// It never mutates the shared state and automatically expires stale episodes.
func KeySuppressed(state QualityState, failureThreshold int, nowMS int64) bool {
	if failureThreshold <= 0 {
		failureThreshold = DefaultPolicyFailureThreshold
	}
	if state.FailureWindowUntilMS > 0 && nowMS > state.FailureWindowUntilMS {
		return false
	}
	return state.ConsecutiveHardFailures >= failureThreshold
}

func QuarantineQuality(state QualityState, atMS int64) QualityState {
	// Kept only as a rolling-upgrade compatibility symbol. V4.4 has no
	// automatic terminal quarantine; callers receive an immediately recoverable
	// open state instead of MaxInt64.
	if state.Epoch == 0 {
		state.Epoch = 1
	}
	state.Phase = QualityOpen
	state.NextProbeAtMS = atMS
	state.SampleDueAtMS = 0
	state.LastUpdatedMS = max64(state.LastUpdatedMS, atMS)
	state.Revision++
	return state
}

// NormalizeAutomaticRecoveryState lazily upgrades legacy terminal automatic
// state. Manual blocks live outside QualityState and therefore remain intact.
// The returned deadline is always finite and due immediately so a durable
// scheduler can take ownership without foreground traffic.
func NormalizeAutomaticRecoveryState(state QualityState, nowMS int64) (QualityState, bool) {
	if state.Phase != QualityQuarantined && state.NextProbeAtMS != math.MaxInt64 {
		return state, false
	}
	if state.Epoch == 0 {
		state.Epoch = 1
	}
	state.Phase = QualityOpen
	state.NextProbeAtMS = nowMS
	state.SampleDueAtMS = 0
	state.WarmingTrafficPPM = 0
	state.SuccessfulWarmSamples = 0
	state.LastUpdatedMS = max64(state.LastUpdatedMS, nowMS)
	state.Revision++
	return state, true
}

func ResetQuality(state QualityState, atMS int64) QualityState {
	epoch := state.Epoch + 1
	revision := state.Revision + 1
	if epoch < 2 {
		epoch = 2
	}
	state = InitialQuality(false)
	state.Epoch = epoch
	state.LastUpdatedMS = atMS
	state.NextProbeAtMS = atMS
	state.Revision = revision
	return state
}

func backoffFor(openCount int, backoffs []int64, retryAfterMS int64) int64 {
	if len(backoffs) == 0 {
		return max64(1, retryAfterMS)
	}
	index := openCount
	if index < 0 {
		index = 0
	}
	if index >= len(backoffs) {
		index = len(backoffs) - 1
	}
	return max64(backoffs[index], retryAfterMS)
}

func warmingTraffic(samples int, stages []WarmingStage) int64 {
	traffic := int64(0)
	for _, stage := range stages {
		if samples < stage.SuccessfulSamples {
			break
		}
		traffic = stage.TrafficPPM
	}
	return traffic
}

func saturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func max64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func validQualityPhase(phase QualityPhase) bool {
	switch phase {
	case QualityUnknown, QualityBootstrap, QualityHealthy, QualityDegraded, QualityOpen, QualityHalfOpen, QualityWarming, QualityQuarantined:
		return true
	default:
		return false
	}
}

func (state QualityState) Validate() error {
	if !validQualityPhase(state.Phase) {
		return fmt.Errorf("smartrouter: unsupported quality phase %q", state.Phase)
	}
	if state.WarmingTrafficPPM < 0 || state.WarmingTrafficPPM > 1_000_000 {
		return errors.New("smartrouter: warming traffic is out of range")
	}
	if state.Epoch == 0 {
		return errors.New("smartrouter: quality epoch must be positive")
	}
	if state.ReliabilityPPM < 0 || state.ReliabilityPPM > 1_000_000 {
		return errors.New("smartrouter: reliability is out of range")
	}
	return nil
}

// DeterministicAdmission uses FNV-1a over stable request/session data. It is
// deterministic and contains no process-local random source.
func DeterministicAdmission(key, routeID string, epoch uint64, trafficPPM int64) bool {
	if trafficPPM <= 0 || key == "" || routeID == "" {
		return false
	}
	if trafficPPM >= 1_000_000 {
		return true
	}
	hash := uint64(14695981039346656037)
	for _, value := range []string{key, "\x00", routeID, "\x00"} {
		for index := 0; index < len(value); index++ {
			hash ^= uint64(value[index])
			hash *= 1099511628211
		}
	}
	for shift := uint(0); shift < 64; shift += 8 {
		hash ^= (epoch >> shift) & 0xff
		hash *= 1099511628211
	}
	return int64(hash%1_000_000) < trafficPPM
}
