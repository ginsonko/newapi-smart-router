package newapibridge

import (
	"errors"
	"testing"
)

func completeHost() HostFingerprint {
	hooks := make([]HookStatus, 0, len(CriticalHooks()))
	for _, id := range CriticalHooks() {
		hooks = append(hooks, HookStatus{ID: id, Present: true, Verified: true, Evidence: "fixture"})
	}
	return HostFingerprint{
		Module:       "github.com/QuantumNous/new-api",
		Version:      "v1.0.0-rc.20",
		Commit:       "synthetic-commit",
		SourceDigest: "sha256:synthetic",
		Protocol:     ProtocolVersion,
		Hooks:        hooks,
	}
}

func TestValidateAcceptsOnlyCompleteVerifiedHost(t *testing.T) {
	result, err := Validate(completeHost())
	if err != nil || !result.Compatible || !result.FullParity {
		t.Fatalf("expected full parity fixture, result=%+v err=%v", result, err)
	}
}

func TestValidateFailsClosedForMissingHook(t *testing.T) {
	host := completeHost()
	host.Hooks = host.Hooks[:len(host.Hooks)-1]
	result, err := Validate(host)
	if !errors.Is(err, ErrIncompatibleHost) || result.FullParity || len(result.Missing) != 1 {
		t.Fatalf("expected one missing hook, result=%+v err=%v", result, err)
	}
}

func TestValidateDoesNotCertifyDirtyOrUnknownHost(t *testing.T) {
	for _, mutate := range []func(*HostFingerprint){
		func(host *HostFingerprint) { host.DirtyWorktree = true },
		func(host *HostFingerprint) { host.UnknownVersion = true },
		func(host *HostFingerprint) { host.Protocol = "future" },
	} {
		host := completeHost()
		mutate(&host)
		result, err := Validate(host)
		if !errors.Is(err, ErrIncompatibleHost) || result.FullParity {
			t.Fatalf("expected fail-closed validation, result=%+v err=%v", result, err)
		}
	}
}

