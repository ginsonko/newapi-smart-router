package smartrouter

// CoverageSelection is an ordered, failure-domain-diverse subset of an
// already strategy-ranked candidate list. Target counts the primary route as
// well as backups: the default target of three means current + two backups.
type CoverageSelection struct {
	Routes          []CertifiedRoute `json:"routes"`
	Target          int              `json:"target"`
	DistinctDomains int              `json:"distinct_domains"`
	Complete        bool             `json:"complete"`
}

// SelectCoverage keeps the caller's ranking while preferring one route from
// each independent failure domain. If fewer independent domains exist, it
// fills the remaining target from same-domain routes so the caller still gets
// the strongest attainable physical redundancy without overstating domain
// diversity.
func SelectCoverage(ranked []CertifiedRoute, target int) CoverageSelection {
	if target < 1 {
		target = 1
	}
	result := CoverageSelection{Target: target, Routes: make([]CertifiedRoute, 0, target)}
	if len(ranked) == 0 {
		return result
	}

	seenRoutes := make(map[string]struct{}, len(ranked))
	seenDomains := make(map[string]struct{}, target)
	deferred := make([]CertifiedRoute, 0, len(ranked))
	for _, route := range ranked {
		if route.RouteID == "" {
			continue
		}
		if _, exists := seenRoutes[route.RouteID]; exists {
			continue
		}
		seenRoutes[route.RouteID] = struct{}{}
		domain := RouteFailureDomain(route)
		if _, exists := seenDomains[domain]; exists {
			deferred = append(deferred, route)
			continue
		}
		seenDomains[domain] = struct{}{}
		result.Routes = append(result.Routes, route)
		if len(result.Routes) == target {
			break
		}
	}
	if len(result.Routes) < target {
		for _, route := range deferred {
			result.Routes = append(result.Routes, route)
			if len(result.Routes) == target {
				break
			}
		}
	}
	result.DistinctDomains = len(seenDomains)
	result.Complete = len(result.Routes) >= target
	return result
}
