package smartrouter

import (
	"errors"
	"fmt"
	"strings"
)

const AutomaticPoolID = "auto-discovered"

type CertifiedRouteCatalog struct {
	Version   string                     `json:"version"`
	Contracts map[string]ContractCatalog `json:"contracts"`
}

type ContractCatalog struct {
	Contract RouteContract            `json:"contract"`
	Pools    map[string]CertifiedPool `json:"pools"`
}

type RouteContract struct {
	ContractID            string `json:"contract_id"`
	CanonicalModel        string `json:"canonical_model"`
	Endpoint              string `json:"endpoint"`
	CapabilityFingerprint string `json:"capability_fingerprint"`
}

type CertifiedPool struct {
	Candidates []CertifiedRoute `json:"candidates"`
}

type CertifiedRoute struct {
	RouteID           string `json:"route_id"`
	ChannelGeneration string `json:"channel_generation"`
	Group             string `json:"group"`
	ChannelID         int    `json:"channel_id"`
	UpstreamModel     string `json:"upstream_model"`
	Capabilities      uint64 `json:"capabilities"`
	MaxContext        int    `json:"max_context"`
	CacheDomain       string `json:"cache_domain"`
	// CacheNamespaceRevision prevents cache warmth learned under one credential
	// pool, channel generation, model mapping, or protocol chain from leaking
	// into a different upstream namespace.
	CacheNamespaceRevision string `json:"cache_namespace_revision,omitempty"`
	CapacityDomain         string `json:"capacity_domain"`
	CredentialDomain       string `json:"credential_domain,omitempty"`
	// FailureDomain identifies routes expected to fail together. It is used
	// only for recovery coverage; dispatch and billing still operate on the
	// exact physical route. When omitted, RouteFailureDomain falls back to the
	// credential/channel identity so old catalogs remain safe.
	FailureDomain  string `json:"failure_domain,omitempty"`
	MaxInflight    int    `json:"max_inflight"`
	StableFallback bool   `json:"stable_fallback"`
	// PassiveRecovery forbids synthetic generation, independent of endpoint.
	PassiveRecovery bool                     `json:"passive_recovery,omitempty"`
	Media           *MediaCapabilityContract `json:"media,omitempty"`
}

func (route CertifiedRoute) CacheNamespaceIdentity() string {
	if strings.TrimSpace(route.CacheNamespaceRevision) != "" {
		return strings.TrimSpace(route.CacheDomain) + ":" + strings.TrimSpace(route.CacheNamespaceRevision)
	}
	return strings.TrimSpace(route.CacheDomain)
}

// RouteFailureDomain returns a conservative, non-empty correlated-failure
// identity. Distinct groups backed by the same channel generation therefore
// count as one recovery domain rather than pretending to be independent
// backups.
func RouteFailureDomain(route CertifiedRoute) string {
	if domain := strings.TrimSpace(route.FailureDomain); domain != "" {
		return domain
	}
	if domain := strings.TrimSpace(route.CredentialDomain); domain != "" {
		return domain
	}
	if route.ChannelID > 0 {
		return fmt.Sprintf("channel:%d:%s", route.ChannelID, strings.TrimSpace(route.ChannelGeneration))
	}
	if domain := strings.TrimSpace(route.CapacityDomain); domain != "" {
		return "capacity:" + domain
	}
	return "route:" + strings.TrimSpace(route.RouteID)
}

// PriceSnapshot is produced outside the core from current New API pricing.
// A missing route price is ineligible rather than being treated as free.
type PriceSnapshot struct {
	Version     string                `json:"version"`
	RatiosPPM   map[string]int64      `json:"ratios_ppm"`
	RoutePrices map[string]RoutePrice `json:"route_prices,omitempty"`
}

func (catalog CertifiedRouteCatalog) Validate() error {
	if catalog.Version == "" {
		return errors.New("smartrouter: catalog version is required")
	}
	if len(catalog.Contracts) == 0 {
		return errors.New("smartrouter: catalog has no contracts")
	}
	routeContracts := make(map[string]string)
	for contractID, entry := range catalog.Contracts {
		if contractID == "" || entry.Contract.ContractID != contractID {
			return fmt.Errorf("smartrouter: contract map key %q does not match contract_id %q", contractID, entry.Contract.ContractID)
		}
		if entry.Contract.CanonicalModel == "" || entry.Contract.Endpoint == "" || entry.Contract.CapabilityFingerprint == "" {
			return fmt.Errorf("smartrouter: contract %q is incomplete", contractID)
		}
		if len(entry.Pools) == 0 {
			return fmt.Errorf("smartrouter: contract %q has no pools", contractID)
		}
		for poolID, pool := range entry.Pools {
			if poolID == "" {
				return fmt.Errorf("smartrouter: contract %q has an empty pool ID", contractID)
			}
			if len(pool.Candidates) == 0 {
				return fmt.Errorf("smartrouter: contract %q pool %q has no routes", contractID, poolID)
			}
			seen := make(map[string]struct{}, len(pool.Candidates))
			for _, route := range pool.Candidates {
				if err := validateRoute(route); err != nil {
					return fmt.Errorf("smartrouter: contract %q pool %q: %w", contractID, poolID, err)
				}
				if _, ok := seen[route.RouteID]; ok {
					return fmt.Errorf("smartrouter: contract %q pool %q repeats route %q", contractID, poolID, route.RouteID)
				}
				seen[route.RouteID] = struct{}{}
				if priorContract, ok := routeContracts[route.RouteID]; ok && priorContract != contractID {
					return fmt.Errorf("smartrouter: route %q appears in contracts %q and %q", route.RouteID, priorContract, contractID)
				}
				routeContracts[route.RouteID] = contractID
			}
		}
	}
	return nil
}

func validateRoute(route CertifiedRoute) error {
	if route.RouteID == "" {
		return errors.New("route_id is required")
	}
	if route.Group == "" {
		return fmt.Errorf("route %q has no group", route.RouteID)
	}
	if route.ChannelID <= 0 {
		return fmt.Errorf("route %q has invalid channel_id", route.RouteID)
	}
	if route.ChannelGeneration == "" {
		return fmt.Errorf("route %q has no channel_generation", route.RouteID)
	}
	if route.UpstreamModel == "" {
		return fmt.Errorf("route %q has no upstream_model", route.RouteID)
	}
	if route.MaxContext <= 0 {
		return fmt.Errorf("route %q has invalid max_context", route.RouteID)
	}
	if route.CacheDomain == "" || route.CapacityDomain == "" {
		return fmt.Errorf("route %q requires cache_domain and capacity_domain", route.RouteID)
	}
	if route.MaxInflight < 0 {
		return fmt.Errorf("route %q has invalid max_inflight", route.RouteID)
	}
	if route.Media != nil {
		if err := route.Media.Validate(); err != nil {
			return fmt.Errorf("route %q has invalid media contract: %w", route.RouteID, err)
		}
	}
	return nil
}

func (snapshot PriceSnapshot) Validate() error {
	if snapshot.Version == "" {
		return errors.New("smartrouter: price snapshot version is required")
	}
	if snapshot.RatiosPPM == nil {
		return errors.New("smartrouter: price snapshot ratios_ppm is required")
	}
	for routeID, ratio := range snapshot.RatiosPPM {
		if routeID == "" || ratio < 0 {
			return fmt.Errorf("smartrouter: invalid price for route %q", routeID)
		}
	}
	for routeID, price := range snapshot.RoutePrices {
		if routeID == "" {
			return errors.New("smartrouter: route price has an empty route ID")
		}
		if err := price.Validate(); err != nil {
			return fmt.Errorf("smartrouter: invalid route price for %q: %w", routeID, err)
		}
	}
	return nil
}
