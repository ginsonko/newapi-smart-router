package newapibridge

import (
	"errors"
	"testing"
)

func TestR98HooksCannotBeInferredFromLegacyHost(t *testing.T) {
	for _, missing := range []HookID{HookPolicyMemory, HookCatalogScope, HookImageJob} {
		t.Run(string(missing), func(test *testing.T) {
			host := completeHost()
			filtered := []HookStatus{}
			for _, hook := range host.Hooks {
				if hook.ID != missing {
					filtered = append(filtered, hook)
				}
			}
			host.Hooks = filtered
			result, err := Validate(host)
			if !errors.Is(err, ErrIncompatibleHost) || result.FullParity || len(result.Missing) != 1 || result.Missing[0] != missing {
				test.Fatalf("legacy host must not imply R98 behavior: result=%+v err=%v", result, err)
			}
		})
	}
}
