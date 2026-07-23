package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/ginsonko/newapi-smart-router/core/smartrouter"
)

type vectorFile struct {
	Version string   `json:"version"`
	Cases   []vector `json:"cases"`
}

type vector struct {
	ID           string        `json:"id"`
	Strategy     string        `json:"strategy"`
	MaxRatioPPM  int64         `json:"max_ratio_ppm"`
	Background  bool          `json:"background_recovery"`
	ManualOrder  []string      `json:"manual_group_order"`
	AttemptedChannels []int   `json:"attempted_channels"`
	Committed    bool          `json:"committed"`
	Routes       []vectorRoute `json:"routes"`
	WantRoute    string        `json:"want_route,omitempty"`
	WantError    string        `json:"want_error,omitempty"`
}

type vectorRoute struct {
	ID             string `json:"id"`
	Group          string `json:"group"`
	ChannelID      int    `json:"channel_id"`
	RatioPPM       int64  `json:"ratio_ppm"`
	Phase          string `json:"phase,omitempty"`
	ReliabilityPPM int64  `json:"reliability_ppm,omitempty"`
	Samples        uint64 `json:"samples,omitempty"`
	ConsecutiveHardFailures int `json:"consecutive_hard_failures,omitempty"`
	FailureWindowUntilMS int64 `json:"failure_window_until_ms,omitempty"`
	NextProbeAtMS  int64  `json:"next_probe_at_ms,omitempty"`
	CapacityFull   bool   `json:"capacity_full,omitempty"`
}

const (
	contractID = "contract-responses-v1"
	poolID     = smartrouter.AutomaticPoolID
)

func buildInput(v vector) smartrouter.PlanInput {
	routes := make([]smartrouter.CertifiedRoute, 0, len(v.Routes))
	prices := make(map[string]int64, len(v.Routes))
	quality := make(map[string]smartrouter.QualityState, len(v.Routes))
	capacity := make(map[string]smartrouter.CapacityState, len(v.Routes))
	groups := make([]string, 0, len(v.Routes))
	for _, item := range v.Routes {
		routes = append(routes, smartrouter.CertifiedRoute{
			RouteID: item.ID, ChannelGeneration: "fixture-generation", Group: item.Group,
			ChannelID: item.ChannelID, UpstreamModel: "fixture-model", Capabilities: 7,
			MaxContext: 128_000, CacheDomain: "cache:" + item.ID,
			CapacityDomain: "capacity:" + item.ID, MaxInflight: 1,
		})
		prices[item.ID] = item.RatioPPM
		if item.Phase != "" {
			quality[item.ID] = smartrouter.QualityState{
				Phase: smartrouter.QualityPhase(item.Phase), StableSinceMS: 1,
				ReliabilityPPM: item.ReliabilityPPM, ReliabilitySamples: item.Samples,
				ConsecutiveHardFailures: item.ConsecutiveHardFailures,
				FailureWindowUntilMS: item.FailureWindowUntilMS, NextProbeAtMS: item.NextProbeAtMS,
			}
		}
		if item.CapacityFull {
			capacity["capacity:"+item.ID] = smartrouter.CapacityState{Known: true, MaxInflight: 1, Inflight: 1}
		}
		groups = append(groups, item.Group)
	}
	return smartrouter.PlanInput{
		NowMS: 100_000, RecoveryEvidenceTTLMS: 3_600_000,
		Request: smartrouter.RouteRequest{
			ContractID: contractID, CanonicalModel: "fixture-model", Endpoint: "responses:stream",
			CapabilityFingerprint: "fixture-capabilities", RequiredCapabilities: 3,
			EstimatedContext: 1024, ReplayClass: smartrouter.ReplaySafeText,
			AdmissionKey: "fixture-admission",
		},
		Policy: smartrouter.Policy{
			Version: smartrouter.PolicyVersion, PolicyID: "fixture-policy", Enabled: true,
			Strategy: smartrouter.Strategy(v.Strategy), MaxEffectiveRatioPPM: v.MaxRatioPPM,
			AllowFutureGroups: true, AllowFutureCertifiedRoutes: true,
			ManualGroupOrder: v.ManualOrder, HealthGuard: true,
			QueuePolicy: smartrouter.QueuePolicy{Mode: smartrouter.QueueModeNoQueue},
		},
		Catalog: smartrouter.CertifiedRouteCatalog{Version: "fixture-catalog", Contracts: map[string]smartrouter.ContractCatalog{
			contractID: {Contract: smartrouter.RouteContract{ContractID: contractID, CanonicalModel: "fixture-model", Endpoint: "responses:stream", CapabilityFingerprint: "fixture-capabilities"}, Pools: map[string]smartrouter.CertifiedPool{poolID: {Candidates: routes}}},
		}},
		Prices: smartrouter.PriceSnapshot{Version: "fixture-price", RatiosPPM: prices},
		Quality: smartrouter.QualitySnapshot{Version: "fixture-quality", Routes: quality},
		Capacity: smartrouter.CapacitySnapshot{Version: "fixture-capacity", Domains: capacity},
		Credentials: smartrouter.CredentialSnapshot{Version: "fixture-credentials", Domains: map[string]smartrouter.CredentialState{}},
		AllowedGroups: groups,
		Attempt: smartrouter.AttemptState{MaxAttempts: 8, AttemptedChannels: v.AttemptedChannels, Committed: v.Committed},
		BackgroundRecovery: v.Background,
	}
}

func main() {
	path := flag.String("vectors", "testdata/planner-v1.json", "conformance vector file")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil { fatal(err) }
	var file vectorFile
	if err := json.Unmarshal(data, &file); err != nil { fatal(err) }
	if file.Version == "" { fatal(errors.New("vector version is required")) }
	for _, vector := range file.Cases {
		result, planErr := smartrouter.Plan(buildInput(vector))
		if vector.WantError != "" {
			if planErr == nil || !containsError(planErr, vector.WantError) { fatal(fmt.Errorf("%s: wanted error %q, got %v", vector.ID, vector.WantError, planErr)) }
			continue
		}
		if planErr != nil { fatal(fmt.Errorf("%s: unexpected plan error: %w", vector.ID, planErr)) }
		if result.RouteID != vector.WantRoute { fatal(fmt.Errorf("%s: wanted route %q, got %q", vector.ID, vector.WantRoute, result.RouteID)) }
	}
	fmt.Printf("conformance PASS: %d vectors (%s)\n", len(file.Cases), file.Version)
}

func containsError(err error, fragment string) bool {
	for current := err; current != nil; {
		if current.Error() == fragment { return true }
		if next, ok := current.(interface{ Unwrap() error }); ok { current = next.Unwrap() } else { break }
	}
	return len(fragment) > 0 && err != nil && (len(err.Error()) >= len(fragment) && containsString(err.Error(), fragment))
}

func containsString(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ { if value[index:index+len(fragment)] == fragment { return true } }
	return false
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "conformance FAIL:", err); os.Exit(1) }
