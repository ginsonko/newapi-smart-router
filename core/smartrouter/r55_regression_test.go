package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R55 keeps a compatible physical route in the snapshot even when a prior
// route-local failure put it in recovery. This prevents a stale health record
// from collapsing a multi-channel request into one (or zero) candidates.
func TestR55SafeTextRetainsRecoveryRoutesAsLastResort(t *testing.T) {
	input := basePlanInput()
	input.Quality.Routes = map[string]QualityState{
		"route-cheap": {
			Phase: QualityOpen, Epoch: 2, LastFailureMS: 1,
			ConsecutiveHardFailures: 1, NextProbeAtMS: input.NowMS + 30_000,
		},
		"route-plus": {
			Phase: QualityOpen, Epoch: 2, LastFailureMS: 2,
			ConsecutiveHardFailures: 1, NextProbeAtMS: input.NowMS + 30_000,
		},
	}

	result, err := Plan(input)
	require.NoError(t, err)
	require.Len(t, result.Candidates, 2)
	assert.Equal(t, "route-cheap", result.Candidates[0].Route.RouteID)
	assert.Equal(t, "route-plus", result.Candidates[1].Route.RouteID)
	assert.Equal(t, AdmissionLastResort, result.Candidates[0].Admission)
	assert.Equal(t, AdmissionLastResort, result.Candidates[1].Admission)
}

// A legacy policy value must not stop a safe stateless request while an
// unattempted physical route remains. The session normally sets the exhaustive
// flag, but the planner contract is tested independently for rolling upgrades.
func TestR55SafeTextIgnoresLegacyAttemptCapWhileRoutesRemain(t *testing.T) {
	input := basePlanInput()
	input.Attempt.MaxAttempts = 1
	input.Attempt.StartedAttempts = 1
	input.Attempt.AttemptedRoutes = []string{"route-cheap"}
	input.Attempt.AttemptedChannels = []int{101}
	input.Attempt.ExhaustEligibleChannels = false

	result, err := Plan(input)
	require.NoError(t, err)
	assert.Equal(t, "route-plus", result.RouteID)
}
