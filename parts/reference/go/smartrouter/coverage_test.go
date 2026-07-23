package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectCoveragePrefersIndependentFailureDomainsWithoutChangingRank(t *testing.T) {
	routes := []CertifiedRoute{
		{RouteID: "cheap-a", ChannelID: 1, ChannelGeneration: "one", FailureDomain: "provider-a"},
		{RouteID: "cheap-a-alias", ChannelID: 2, ChannelGeneration: "two", FailureDomain: "provider-a"},
		{RouteID: "plus-b", ChannelID: 3, ChannelGeneration: "three", FailureDomain: "provider-b"},
		{RouteID: "pro-c", ChannelID: 4, ChannelGeneration: "four", FailureDomain: "provider-c"},
	}

	selection := SelectCoverage(routes, 3)
	require.Len(t, selection.Routes, 3)
	assert.Equal(t, []string{"cheap-a", "plus-b", "pro-c"}, []string{
		selection.Routes[0].RouteID, selection.Routes[1].RouteID, selection.Routes[2].RouteID,
	})
	assert.Equal(t, 3, selection.DistinctDomains)
	assert.True(t, selection.Complete)
}

func TestSelectCoverageFallsBackToSameDomainWithoutClaimingDiversity(t *testing.T) {
	routes := []CertifiedRoute{
		{RouteID: "a", FailureDomain: "shared"},
		{RouteID: "b", FailureDomain: "shared"},
	}
	selection := SelectCoverage(routes, 3)
	require.Len(t, selection.Routes, 2)
	assert.Equal(t, 3, selection.Target)
	assert.Equal(t, 1, selection.DistinctDomains)
	assert.False(t, selection.Complete, "two routes cannot satisfy a current-plus-two-backups target")
}

func TestSelectCoverageReportsAnEmptyCoverageGap(t *testing.T) {
	selection := SelectCoverage(nil, 3)
	assert.Equal(t, 3, selection.Target)
	assert.Empty(t, selection.Routes)
	assert.Zero(t, selection.DistinctDomains)
	assert.False(t, selection.Complete)
}
