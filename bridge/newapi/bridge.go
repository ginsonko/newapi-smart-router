// Package newapibridge defines the trusted host hooks required by Smart Router.
// It deliberately contains no reflection-based patching or production writes.
package newapibridge

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ginsonko/newapi-smart-router/core/smartrouter"
)

const ProtocolVersion = "bridge-spi-v1alpha4"

type HookID string

const (
	HookPostAuthPolicy  HookID = "HOOK-AUTH-001"
	HookRequestContract HookID = "HOOK-CONTRACT-001"
	HookExactRoute      HookID = "HOOK-ROUTE-001"
	HookPriceSnapshot   HookID = "HOOK-PRICE-001"
	HookCommitBoundary  HookID = "HOOK-COMMIT-001"
	HookOutcome         HookID = "HOOK-OUTCOME-001"
	HookBilling         HookID = "HOOK-BILL-001"
	HookLog             HookID = "HOOK-LOG-001"
	HookMedia           HookID = "HOOK-MEDIA-001"
	HookPolicyMemory    HookID = "HOOK-POLICY-MEMORY-001"
	HookCatalogScope    HookID = "HOOK-CATALOG-SCOPE-001"
	HookImageJob        HookID = "HOOK-IMAGE-JOB-001"
)

var criticalHooks = []HookID{
	HookPostAuthPolicy,
	HookRequestContract,
	HookExactRoute,
	HookPriceSnapshot,
	HookCommitBoundary,
	HookOutcome,
	HookBilling,
	HookLog,
	HookMedia,
	HookPolicyMemory,
	HookCatalogScope,
	HookImageJob,
}

type HookStatus struct {
	ID       HookID `json:"id"`
	Present  bool   `json:"present"`
	Verified bool   `json:"verified"`
	Evidence string `json:"evidence,omitempty"`
}

type HostFingerprint struct {
	Module               string       `json:"module"`
	Version              string       `json:"version"`
	Commit               string       `json:"commit"`
	SourceDigest         string       `json:"source_digest"`
	Protocol             string       `json:"protocol"`
	Hooks                []HookStatus `json:"hooks"`
	DirtyWorktree        bool         `json:"dirty_worktree"`
	UnknownVersion       bool         `json:"unknown_version"`
	WebArtifactDigest    string       `json:"web_artifact_digest"`
	WorkerArtifactDigest string       `json:"worker_artifact_digest"`
}

type Validation struct {
	Compatible   bool     `json:"compatible"`
	FullParity   bool     `json:"full_parity"`
	Missing      []HookID `json:"missing"`
	Unverified   []HookID `json:"unverified"`
	ReasonCodes  []string `json:"reason_codes"`
	HostRevision string   `json:"host_revision"`
}

// Planner is the only core decision entry point a host Bridge needs. The host
// remains responsible for immutable snapshots and trusted side effects.
type Planner func(smartrouter.PlanInput) (smartrouter.PlanResult, error)

var ErrIncompatibleHost = errors.New("smart router bridge: incompatible host")

// Validate checks only declared evidence. It does not guess a Hook from a
// filename and does not elevate an unknown or dirty host to Certified status.
func Validate(host HostFingerprint) (Validation, error) {
	result := Validation{
		Missing:      []HookID{},
		Unverified:   []HookID{},
		ReasonCodes:  []string{},
		HostRevision: host.Commit,
	}
	statuses := make(map[HookID]HookStatus, len(host.Hooks))
	for _, status := range host.Hooks {
		statuses[status.ID] = status
	}
	for _, id := range criticalHooks {
		status, ok := statuses[id]
		if !ok || !status.Present {
			result.Missing = append(result.Missing, id)
			continue
		}
		if !status.Verified {
			result.Unverified = append(result.Unverified, id)
		}
	}
	sort.Slice(result.Missing, func(i, j int) bool { return result.Missing[i] < result.Missing[j] })
	sort.Slice(result.Unverified, func(i, j int) bool { return result.Unverified[i] < result.Unverified[j] })
	if host.Protocol != ProtocolVersion {
		result.ReasonCodes = append(result.ReasonCodes, "bridge_protocol_mismatch")
	}
	if host.UnknownVersion {
		result.ReasonCodes = append(result.ReasonCodes, "unknown_host_version")
	}
	if host.DirtyWorktree {
		result.ReasonCodes = append(result.ReasonCodes, "dirty_host_requires_review")
	}
	if host.Module == "" || host.Commit == "" || host.SourceDigest == "" {
		result.ReasonCodes = append(result.ReasonCodes, "incomplete_host_fingerprint")
	}
	if host.WebArtifactDigest == "" || host.WorkerArtifactDigest == "" {
		result.ReasonCodes = append(result.ReasonCodes, "incomplete_runtime_artifact_fingerprint")
	} else if host.WebArtifactDigest != host.WorkerArtifactDigest {
		result.ReasonCodes = append(result.ReasonCodes, "runtime_artifact_mismatch")
	}
	result.Compatible = len(result.Missing) == 0 && len(result.ReasonCodes) == 0
	result.FullParity = result.Compatible && len(result.Unverified) == 0
	if !result.FullParity {
		return result, fmt.Errorf("%w: missing=%d unverified=%d reasons=%d", ErrIncompatibleHost, len(result.Missing), len(result.Unverified), len(result.ReasonCodes))
	}
	return result, nil
}

func CriticalHooks() []HookID {
	return append([]HookID(nil), criticalHooks...)
}
