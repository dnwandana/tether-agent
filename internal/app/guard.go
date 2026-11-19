package app

import "github.com/dnwandana/tether-agent/internal/security"

// sendGuard returns the model_guard of unattended sends. It returns nil when outbound.guard is
// false. An empty outbound.guard_provider uses the primary chain.
func (a *App) sendGuard() security.SendGuarder {
	if !a.Cfg.Outbound.Guard {
		return nil
	}
	prov := a.Prov
	if name := a.Cfg.Outbound.GuardProvider; name != "" {
		prov = a.Set.ChainFor(name, "")
	}
	return security.SendGuard{Prov: prov, Record: a.recordUsage}
}
