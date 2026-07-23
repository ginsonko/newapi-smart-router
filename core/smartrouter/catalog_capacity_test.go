package smartrouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogRejectsNegativeRouteCapacityLimit(t *testing.T) {
	catalog := testCatalog()
	entry := catalog.Contracts[testContractID]
	pool := entry.Pools[testPoolID]
	pool.Candidates[0].MaxInflight = -1
	entry.Pools[testPoolID] = pool
	catalog.Contracts[testContractID] = entry

	require.Error(t, catalog.Validate())
}
