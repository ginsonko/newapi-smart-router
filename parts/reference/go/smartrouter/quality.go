package smartrouter

import (
	"errors"
	"fmt"
	"math"
	"strings"
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

// MaximumRecoveryProbeIntervalMS is the hard recovery contract shared by the
// state reducer and the bounded background scheduler. A failed route may stay
// unavailable, but a legacy backoff or Retry-After value must never leave it
// without another recovery opportunity for more than five minutes.
const MaximumRecoveryProbeIntervalMS int64 = 5 * 60_000
const MaximumMediaRecoveryIntervalMS int64 = 60 * 60_000

// DefaultSharedOpenFailureThreshold is a site-wide health invariant. One
// confirmed request failure already represents two serial attempts against the
// same physical route, but it must not remove that route for every user. Three
// distinct confirmed request observations are required before shared health is
// opened; the first two remain visible as degraded evidence.
const DefaultSharedOpenFailureThreshold = 3
const DefaultImageFailureWindowMS int64 = 3_600_000
const DefaultImageFailureThreshold = 11

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
	ImageHealthTracked          bool           `json:"image_health_tracked,omitempty"`
	ImageUnavailable            bool           `json:"image_unavailable,omitempty"`
	ImageUnavailableAtMS        int64          `json:"image_unavailable_at_ms,omitempty"`
	ImageAttemptEvidence        string         `json:"image_attempt_evidence,omitempty"`
	ImageFailureWindowMS        int64          `json:"image_failure_window_ms,omitempty"`
	ImageFailureThreshold       int            `json:"image_failure_threshold,omitempty"`
	ImageLastSuccessMS          int64          `json:"image_last_success_ms,omitempty"`
	ImageEvidencePrunedBeforeMS int64          `json:"image_evidence_pruned_before_ms,omitempty"`
	DemandRecovery              bool           `json:"demand_recovery,omitempty"`
	DemandRecoveryFailures      int            `json:"demand_recovery_failures,omitempty"`
	PassiveRecovery             bool           `json:"passive_recovery,omitempty"`
	PassiveRecoveryFailures     int            `json:"passive_recovery_failures,omitempty"`
	Phase                       QualityPhase   `json:"phase"`
	Revision                    uint64         `json:"revision"`
	Epoch                       uint64         `json:"epoch"`
	ConsecutiveSuccesses        int            `json:"consecutive_successes"`
	ConsecutiveHardFailures     int            `json:"consecutive_hard_failures"`
	TotalSuccesses              uint64         `json:"total_successes"`
	TotalHardFailures           uint64         `json:"total_hard_failures"`
	RealSuccesses               uint64         `json:"real_successes"`
	RealHardFailures            uint64         `json:"real_hard_failures"`
	TotalProbeFailures          uint64         `json:"total_probe_failures"`
	ReliabilityPPM              int64          `json:"reliability_ppm"`
	ReliabilitySamples          uint64         `json:"reliability_samples"`
	ProbeFailures               int            `json:"probe_failures"`
	SuccessfulWarmSamples       int            `json:"successful_warm_samples"`
	WarmingTrafficPPM           int64          `json:"warming_traffic_ppm"`
	WarmingEpoch                uint64         `json:"warming_epoch"`
	OpenCount                   int            `json:"open_count"`
	LastUpdatedMS               int64          `json:"last_updated_ms"`
	FirstFailureMS              int64          `json:"first_failure_ms"`
	LastFailureMS               int64          `json:"last_failure_ms"`
	FailureWindowUntilMS        int64          `json:"failure_window_until_ms"`
	LastSuccessMS               int64          `json:"last_success_ms"`
	LastRealSuccessMS           int64          `json:"last_real_success_ms"`
	LastRealFailureMS           int64          `json:"last_real_failure_ms"`
	LastRealOutcome             QualityOutcome `json:"last_real_outcome"`
	LastOutcome                 QualityOutcome `json:"last_outcome"`
	LastHTTPStatus              int            `json:"last_http_status,omitempty"`
	LastErrorSummary            string         `json:"last_error_summary,omitempty"`
	// Probe evidence is kept separately from ordinary traffic evidence so the
	// health UI can distinguish a real request from a background recovery check.
	LastProbeAtMS         int64          `json:"last_probe_at_ms"`
	LastProbeOutcome      QualityOutcome `json:"last_probe_outcome,omitempty"`
	LastProbeHTTPStatus   int            `json:"last_probe_http_status,omitempty"`
	LastProbeErrorSummary string         `json:"last_probe_error_summary,omitempty"`
	ProbeSuccesses        uint64         `json:"probe_successes"`
	// LastHardFailureObservation stores a bounded, one-way request digest. It
	// prevents retries, alias fan-out and reconciliation replay from counting one
	// confirmed request more than once. It never contains a raw request ID.
	LastHardFailureObservation string `json:"last_hard_failure_observation,omitempty"`
	LastSuccessObservation     string `json:"last_success_observation,omitempty"`
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
	OutcomeImagePending    QualityOutcome = "image_pending"
	OutcomeSuccess         QualityOutcome = "success"
	OutcomeInfrastructure  QualityOutcome = "infrastructure_failure"
	OutcomeCredential      QualityOutcome = "credential_failure"
	OutcomeCapacityLimited QualityOutcome = "capacity_limited"
	OutcomeUserRejected    QualityOutcome = "user_rejected"
	OutcomeContentRejected QualityOutcome = "content_rejected"
	OutcomeClientCancelled QualityOutcome = "client_cancelled"
)

type QualityEvent struct {
	ImageAttempt    bool           `json:"image_attempt,omitempty"`
	DemandRecovery  bool           `json:"demand_recovery,omitempty"`
	PassiveRecovery bool           `json:"passive_recovery,omitempty"`
	AtMS            int64          `json:"at_ms"`
	StartedAtMS     int64          `json:"started_at_ms,omitempty"`
	ObservationID   string         `json:"-"`
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
	Epoch                  uint64 `json:"epoch"`
	ConsecutiveFailures    int    `json:"consecutive_failures"`
	BlockedUntilMS         int64  `json:"blocked_until_ms"`
	LastFailureMS          int64  `json:"last_failure_ms"`
	LastFailureObservation string `json:"last_failure_observation,omitempty"`
	LastSuccessMS          int64  `json:"last_success_ms"`
	Revision               uint64 `json:"revision"`
}

type WarmingStage struct {
	SuccessfulSamples int   `json:"successful_samples"`
	TrafficPPM        int64 `json:"traffic_ppm"`
}

type QualityConfig struct {
	ImageFailureWindowMS          int64          `json:"image_failure_window_ms"`
	ImageFailureThreshold         int            `json:"image_failure_threshold"`
	TextRecoveryProbeAfterMS      int64          `json:"text_recovery_probe_after_ms"`
	TextRecoveryInitialBackoffMS  int64          `json:"text_recovery_initial_backoff_ms"`
	TextRecoveryMaxBackoffMS      int64          `json:"text_recovery_max_backoff_ms"`
	MediaRecoveryInitialBackoffMS int64          `json:"media_recovery_initial_backoff_ms"`
	MediaRecoveryMaxBackoffMS     int64          `json:"media_recovery_max_backoff_ms"`
	SharedOpenFailureThreshold    int            `json:"shared_open_failure_threshold"`
	HardFailureWindowMS           int64          `json:"hard_failure_window_ms"`
	FastRecoveryIntervalMS        int64          `json:"fast_recovery_interval_ms"`
	FastRecoveryWindowMS          int64          `json:"fast_recovery_window_ms"`
	SlowRecoveryBackoffMS         []int64        `json:"slow_recovery_backoff_ms"`
	OpenBackoffMS                 []int64        `json:"open_backoff_ms"`
	CredentialBackoffMS           int64          `json:"credential_backoff_ms"`
	DegradedRecoverySuccesses     int            `json:"degraded_recovery_successes"`
	HealthyWarmSamples            int            `json:"healthy_warm_samples"`
	FastCanaryMinSamples          int            `json:"fast_canary_min_samples"`
	FastCanaryMinReliabilityPPM   int64          `json:"fast_canary_min_reliability_ppm"`
	WarmingSampleIntervalMS       int64          `json:"warming_sample_interval_ms"`
	WarmingStages                 []WarmingStage `json:"warming_stages"`
}

func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		ImageFailureWindowMS:        DefaultImageFailureWindowMS,
		ImageFailureThreshold:       DefaultImageFailureThreshold,
		SharedOpenFailureThreshold:  DefaultSharedOpenFailureThreshold,
		HardFailureWindowMS:         60_000,
		FastRecoveryIntervalMS:      60_000,
		FastRecoveryWindowMS:        60 * 60_000,
		SlowRecoveryBackoffMS:       []int64{MaximumRecoveryProbeIntervalMS},
		OpenBackoffMS:               []int64{10_000, 20_000, 40_000, 60_000, 120_000, 240_000, MaximumRecoveryProbeIntervalMS},
		CredentialBackoffMS:         MaximumRecoveryProbeIntervalMS,
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
	if config.ImageFailureWindowMS < 0 || config.ImageFailureWindowMS > 86_400_000 ||
		config.ImageFailureThreshold < 0 || config.ImageFailureThreshold > 100_000 {
		return errors.New("smartrouter: invalid image failure window or threshold")
	}
	if (config.SharedOpenFailureThreshold != 0 && (config.SharedOpenFailureThreshold < 2 || config.SharedOpenFailureThreshold > 10)) ||
		config.HardFailureWindowMS <= 0 || config.FastRecoveryIntervalMS <= 0 ||
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
		if delay <= 0 || delay > MaximumRecoveryProbeIntervalMS {
			return errors.New("smartrouter: open backoffs must be positive and at most five minutes")
		}
	}
	if len(config.SlowRecoveryBackoffMS) == 0 {
		return errors.New("smartrouter: at least one slow recovery backoff is required")
	}
	previousDelay := int64(0)
	for _, delay := range config.SlowRecoveryBackoffMS {
		if delay <= 0 || delay < previousDelay || delay > MaximumRecoveryProbeIntervalMS {
			return errors.New("smartrouter: slow recovery backoffs must be positive, non-decreasing and at most five minutes")
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
		state.LastHardFailureObservation == "" && state.LastSuccessObservation == "" &&
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

// ReduceQuality applies one already-classified attempt outcome. Request-local
// user/content rejections and cancellation leave shared health intact. A
// route-attributable infrastructure, credential, or capacity failure first
// degrades shared health. Three distinct confirmed requests are required to
// open it; one bounded background probe owns recovery after that point.
func ReduceQuality(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	if event.ImageAttempt {
		return reduceImageQuality(state, event, config)
	}
	if event.DemandRecovery {
		state.DemandRecovery = true
		if !event.SyntheticProbe {
			event.Probe = false
		}
	}
	if event.PassiveRecovery && !event.SyntheticProbe {
		// A real recovery request owns a probe lease but is still user evidence.
		event.Probe = false
		state.PassiveRecovery = true
	}
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
	case OutcomeUserRejected, OutcomeContentRejected, OutcomeClientCancelled:
		return state
	case OutcomeSuccess:
		return reduceQualitySuccess(state, event, config)
	case OutcomeCredential:
		return reduceQualityFailure(state, event, event.RetryAfterMS, config)
	case OutcomeInfrastructure, OutcomeCapacityLimited:
		return reduceQualityFailure(state, event, event.RetryAfterMS, config)
	default:
		return state
	}
}

func reduceQualityFailure(state QualityState, event QualityEvent, retryAfterMS int64, config QualityConfig) QualityState {
	hadConfirmedFailure := state.ConsecutiveHardFailures > 0 || state.RealHardFailures > 0 ||
		state.Phase == QualityOpen || state.Phase == QualityHalfOpen
	if event.Probe && !hadConfirmedFailure {
		// A manual or cold-start probe is observational when the route has no
		// confirmed outage. Keep a previously proven route healthy (or a virgin
		// route unknown) while retaining separate probe telemetry. In particular,
		// a local probe transport failure must never create shared outage evidence.
		state.TotalProbeFailures++
		state.ProbeFailures++
		state.LastUpdatedMS = max64(state.LastUpdatedMS, event.AtMS)
		state.Revision++
		if state.LastSuccessMS > 0 || state.LastRealSuccessMS > 0 {
			state.Phase = QualityHealthy
			state.WarmingTrafficPPM = 1_000_000
		} else {
			state.Phase = QualityUnknown
			state.WarmingTrafficPPM = 0
		}
		state.NextProbeAtMS = 0
		state.SampleDueAtMS = 0
		return state
	}
	next, counted := recordHardFailureEvidence(state, event, config)
	if event.PassiveRecovery && !event.SyntheticProbe {
		if !counted {
			return next
		}
		initial, maximum := config.MediaRecoveryInitialBackoffMS, config.MediaRecoveryMaxBackoffMS
		if maximum <= 0 || maximum > MaximumMediaRecoveryIntervalMS {
			maximum = MaximumMediaRecoveryIntervalMS
		}
		if initial <= 0 {
			initial = 60_000
		}
		if initial > maximum {
			initial = maximum
		}
		delay := initial
		for i := 0; i < next.PassiveRecoveryFailures && delay < maximum; i++ {
			if delay > maximum/2 {
				delay = maximum
			} else {
				delay *= 2
			}
		}
		delay = min(maximum, max64(delay, retryAfterMS))
		if next.PassiveRecoveryFailures < 63 {
			next.PassiveRecoveryFailures++
		}
		next.Phase = QualityOpen
		next.Revision++
		next.OpenCount++
		next.WarmingTrafficPPM = 0
		next.NextProbeAtMS = saturatingAdd(event.AtMS, delay)
		next.SampleDueAtMS = 0
		return next
	}
	if event.DemandRecovery {
		if !counted {
			return next
		}
		initial, maximum, probeAfter := config.TextRecoveryInitialBackoffMS, config.TextRecoveryMaxBackoffMS, config.TextRecoveryProbeAfterMS
		if initial <= 0 {
			initial = 2000
		}
		if maximum < 300_000 || maximum > 600_000 {
			maximum = 600_000
		}
		if probeAfter <= 0 {
			probeAfter = 300_000
		}
		delay := initial
		for i := 0; i < next.DemandRecoveryFailures && delay < maximum; i++ {
			delay = min(maximum, delay*2)
		}
		if event.SyntheticProbe {
			delay = 300_000
			if next.ProbeFailures > 1 {
				delay = 600_000
			}
		}
		delay = min(maximum, max64(delay, retryAfterMS))
		nextDue := saturatingAdd(event.AtMS, delay)
		probeAt := saturatingAdd(next.RecoveryStartedMS, probeAfter)
		if event.AtMS < probeAt && nextDue > probeAt {
			nextDue = probeAt
		}
		if next.DemandRecoveryFailures < 63 {
			next.DemandRecoveryFailures++
		}
		next.Phase = QualityOpen
		next.Revision++
		next.OpenCount++
		next.WarmingTrafficPPM = 0
		next.NextProbeAtMS = nextDue
		next.SampleDueAtMS = 0
		return next
	}
	if event.Probe {
		return reopenQualityAfterProbeFailure(next, event.AtMS, retryAfterMS, config)
	}
	if !counted {
		return next
	}
	if next.ConsecutiveHardFailures < effectiveSharedOpenFailureThreshold(config) {
		return degradeQuality(next, event.AtMS)
	}
	return openQuality(next, event.AtMS, retryAfterMS)
}

func effectiveSharedOpenFailureThreshold(config QualityConfig) int {
	if config.SharedOpenFailureThreshold >= 2 {
		return config.SharedOpenFailureThreshold
	}
	return DefaultSharedOpenFailureThreshold
}

func recordHardFailureEvidence(state QualityState, event QualityEvent, config QualityConfig) (QualityState, bool) {
	observationID := strings.TrimSpace(event.ObservationID)
	if !event.Probe {
		if observationID != "" && observationID == state.LastHardFailureObservation {
			return state, false
		}
		// A request which started before the latest proven success is stale
		// failure evidence. It may finish later, but it cannot reopen a route
		// which another user has already proved healthy.
		if state.LastSuccessMS > 0 && event.StartedAtMS > 0 && event.StartedAtMS <= state.LastSuccessMS {
			return state, false
		}
	}
	if state.RecoveryStartedMS == 0 {
		state.RecoveryStartedMS = event.AtMS
	}
	if state.FirstFailureMS == 0 {
		state.FirstFailureMS = event.AtMS
	}
	if event.Probe {
		state.TotalProbeFailures++
		state.ProbeFailures++
		state.LastFailureMS = event.AtMS
		state.LastOutcome = event.Outcome
		state.LastHTTPStatus = event.HTTPStatus
		state.LastErrorSummary = event.ErrorSummary
		return state, true
	}
	withinWindow := state.LastFailureMS > 0 && event.AtMS-state.LastFailureMS <= config.HardFailureWindowMS
	if !withinWindow {
		state.ConsecutiveHardFailures = 1
	} else {
		state.ConsecutiveHardFailures++
	}
	state.TotalHardFailures++
	state.RealHardFailures++
	state.ReliabilityPPM = updateReliabilityPPM(state.ReliabilityPPM, false, state.ReliabilitySamples)
	state.ReliabilitySamples++
	state.ConsecutiveSuccesses = 0
	state.LastFailureMS = event.AtMS
	state.LastRealFailureMS = event.AtMS
	state.LastRealOutcome = event.Outcome
	state.LastHardFailureObservation = observationID
	state.FailureWindowUntilMS = saturatingAdd(event.AtMS, config.HardFailureWindowMS)
	state.LastOutcome = event.Outcome
	state.LastHTTPStatus = event.HTTPStatus
	state.LastErrorSummary = event.ErrorSummary
	return state, true
}

func reduceQualitySuccess(state QualityState, event QualityEvent, config QualityConfig) QualityState {
	atMS := event.AtMS
	observationID := strings.TrimSpace(event.ObservationID)
	if !event.Probe && observationID != "" && observationID == state.LastSuccessObservation {
		return state
	}
	state.Revision++
	state.LastUpdatedMS = atMS
	state.ProbeFailures = 0
	state.PassiveRecoveryFailures = 0
	state.DemandRecoveryFailures = 0
	state.OpenCount = 0
	state.SlowRecoveryAttempts = 0
	state.NextProbeAtMS = 0
	state.LastSuccessMS = atMS
	state.LastOutcome = OutcomeSuccess
	state.LastHTTPStatus = 0
	state.LastErrorSummary = ""
	state.ConsecutiveHardFailures = 0
	state.LastHardFailureObservation = ""
	state.FirstFailureMS = 0
	state.RecoveryStartedMS = 0
	state.FailureWindowUntilMS = 0
	state.SuccessfulWarmSamples = 0
	state.WarmingTrafficPPM = 1_000_000
	state.SampleDueAtMS = 0
	state.StableSinceMS = atMS
	state.Phase = QualityHealthy
	if event.Probe {
		state.ConsecutiveSuccesses = 0
	} else {
		// A real success invalidates every probe lease planned from an older
		// health epoch. Real foreground requests are not fenced by this epoch, so
		// concurrent successful requests continue to contribute evidence.
		state.Epoch++
		if state.Epoch == 0 {
			state.Epoch = 1
		}
		state.RealSuccesses++
		state.LastRealSuccessMS = atMS
		state.LastRealOutcome = event.Outcome
		state.LastSuccessObservation = observationID
		state.TotalSuccesses++
		state.ReliabilityPPM = updateReliabilityPPM(state.ReliabilityPPM, true, state.ReliabilitySamples)
		state.ReliabilitySamples++
		state.ConsecutiveSuccesses++
	}
	return state
}

func degradeQuality(state QualityState, atMS int64) QualityState {
	state.Phase = QualityDegraded
	state.Revision++
	state.ConsecutiveSuccesses = 0
	state.WarmingTrafficPPM = 1_000_000
	state.SuccessfulWarmSamples = 0
	state.LastUpdatedMS = atMS
	state.NextProbeAtMS = 0
	state.SampleDueAtMS = 0
	return state
}

func openQuality(state QualityState, atMS, retryAfterMS int64) QualityState {
	delayMS := retryAfterMS
	if delayMS < 0 {
		delayMS = 0
	}
	if delayMS > MaximumRecoveryProbeIntervalMS {
		delayMS = MaximumRecoveryProbeIntervalMS
	}
	state.Phase = QualityOpen
	state.Revision++
	state.OpenCount++
	state.SlowRecoveryAttempts = 0
	state.ConsecutiveSuccesses = 0
	state.WarmingTrafficPPM = 0
	state.SuccessfulWarmSamples = 0
	state.LastFailureMS = atMS
	state.LastUpdatedMS = atMS
	state.NextProbeAtMS = saturatingAdd(atMS, delayMS)
	state.SampleDueAtMS = 0
	return state
}

func reopenQualityAfterProbeFailure(state QualityState, atMS, retryAfterMS int64, config QualityConfig) QualityState {
	delayMS := backoffFor(state.SlowRecoveryAttempts, config.OpenBackoffMS, retryAfterMS)
	state.Phase = QualityOpen
	state.Revision++
	state.OpenCount++
	state.SlowRecoveryAttempts++
	state.ConsecutiveSuccesses = 0
	state.WarmingTrafficPPM = 0
	state.SuccessfulWarmSamples = 0
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

// NormalizeAutomaticRecoveryState reduces rolling-upgrade encodings to the
// observable three-state contract. New/unevidenced routes are unknown and
// usable, newer success evidence is healthy immediately, and newer failure
// evidence is unavailable until one recovery probe succeeds. Manual blocks
// live outside QualityState and remain intact.
func NormalizeAutomaticRecoveryState(state QualityState, nowMS int64) (QualityState, bool) {
	return NormalizeAutomaticRecoveryStateWithConfig(state, nowMS, DefaultQualityConfig())
}

// NormalizeAutomaticRecoveryStateWithConfig applies rolling-upgrade
// normalization without replacing a site's configured shared-open threshold
// with the default value.
func NormalizeAutomaticRecoveryStateWithConfig(state QualityState, nowMS int64, config QualityConfig) (QualityState, bool) {
	if state.ImageHealthTracked {
		return NormalizeImageQuality(state, nowMS, config)
	}
	original := state
	openThreshold := effectiveSharedOpenFailureThreshold(config)
	if state.Epoch == 0 {
		state.Epoch = 1
	}
	lastSuccessMS := max64(state.LastSuccessMS, state.LastRealSuccessMS)
	lastFailureMS := max64(state.LastFailureMS, state.LastRealFailureMS)
	successNewer := lastSuccessMS > 0 && (lastSuccessMS > lastFailureMS ||
		(lastSuccessMS == lastFailureMS && state.LastOutcome == OutcomeSuccess))
	failureNewer := lastFailureMS > 0 && !successNewer

	switch state.Phase {
	case QualityHealthy:
		// An explicitly healthy state remains usable until newer failure
		// evidence is observed. This preserves a valid stable-fallback state
		// whose historical timestamps were not persisted by an older worker.
		if failureNewer {
			if state.ConsecutiveHardFailures >= openThreshold {
				state.Phase = QualityOpen
			} else {
				state.Phase = QualityDegraded
			}
		}
	case QualityUnknown:
		if successNewer {
			state.Phase = QualityHealthy
		} else if failureNewer {
			if state.ConsecutiveHardFailures >= openThreshold {
				state.Phase = QualityOpen
			} else {
				state.Phase = QualityDegraded
			}
		}
	case QualityBootstrap, QualityWarming:
		// Legacy phases are not evidence. A route is unavailable only when a
		// concrete failure (or an explicit hard-failure counter) is newer than
		// its last success; otherwise a state imported from an older release
		// starts neutral and immediately usable.
		switch {
		case successNewer:
			state.Phase = QualityHealthy
		case failureNewer && state.ConsecutiveHardFailures >= openThreshold:
			state.Phase = QualityOpen
		case failureNewer || (state.Phase == QualityDegraded && hasHardFailureEvidence(state)):
			state.Phase = QualityDegraded
		default:
			state.Phase = QualityUnknown
		}
	case QualityDegraded:
		// Degraded is a deliberate reducer decision under the current site
		// threshold. Do not reinterpret its counter with a hard-coded threshold
		// during rolling upgrades or read-only snapshot normalization.
		if successNewer {
			state.Phase = QualityHealthy
		} else if !failureNewer && !hasHardFailureEvidence(state) {
			state.Phase = QualityUnknown
		}
	case QualityOpen:
		if successNewer {
			state.Phase = QualityHealthy
		} else if !failureNewer && !hasOpenRecoveryEvidence(state) {
			// A stale open marker without any failure/probe evidence is a
			// release-compatibility artifact, not a reason to paint a route red.
			state.Phase = QualityUnknown
		}
	case QualityHalfOpen:
		if successNewer {
			state.Phase = QualityHealthy
		}
	case QualityQuarantined:
		// Manual/contract quarantine is represented separately by the block
		// store. Keep this legacy phase unavailable until an explicit reset or
		// a successful probe, but never let MaxInt64 make recovery impossible.
		state.Phase = QualityOpen
	}
	if state.NextProbeAtMS == math.MaxInt64 {
		state.NextProbeAtMS = nowMS
	}
	if state.Phase == QualityOpen {
		if state.NextProbeAtMS <= 0 {
			state.NextProbeAtMS = nowMS
		}
		maximum := MaximumRecoveryProbeIntervalMS
		if state.DemandRecovery {
			maximum = 600_000
		}
		if state.PassiveRecovery {
			maximum = MaximumMediaRecoveryIntervalMS
		}
		maximumDue := saturatingAdd(nowMS, maximum)
		if state.NextProbeAtMS > maximumDue {
			state.NextProbeAtMS = maximumDue
		}
	} else if state.Phase != QualityHalfOpen {
		state.NextProbeAtMS = 0
	}
	state.SampleDueAtMS = 0
	if state.Phase == QualityHealthy || state.Phase == QualityDegraded {
		state.WarmingTrafficPPM = 1_000_000
	} else {
		state.WarmingTrafficPPM = 0
	}
	state.SuccessfulWarmSamples = 0
	if state == original {
		return state, false
	}
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
	state.NextProbeAtMS = 0
	state.Revision = revision
	return state
}

func backoffFor(openCount int, backoffs []int64, retryAfterMS int64) int64 {
	if len(backoffs) == 0 {
		delay := max64(1, retryAfterMS)
		if delay > MaximumRecoveryProbeIntervalMS {
			return MaximumRecoveryProbeIntervalMS
		}
		return delay
	}
	index := openCount
	if index < 0 {
		index = 0
	}
	if index >= len(backoffs) {
		index = len(backoffs) - 1
	}
	delay := max64(backoffs[index], retryAfterMS)
	if delay > MaximumRecoveryProbeIntervalMS {
		return MaximumRecoveryProbeIntervalMS
	}
	return delay
}

func hasHardFailureEvidence(state QualityState) bool {
	return state.LastFailureMS > 0 || state.LastRealFailureMS > 0 ||
		state.ConsecutiveHardFailures > 0 || state.TotalHardFailures > 0 ||
		state.RealHardFailures > 0 || state.ProbeFailures > 0 ||
		state.TotalProbeFailures > 0 || state.RecoveryStartedMS > 0
}

func hasOpenRecoveryEvidence(state QualityState) bool {
	return hasHardFailureEvidence(state) || state.NextProbeAtMS > 0 || state.OpenCount > 0
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
