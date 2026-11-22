package security

// FitScope returns the scope that an approval request permits for the answer s. With
// noSession, "this session" and "always" give "once". noSession also hides "always",
// because "always" is wider than "this session". Else with noAlways, "always" gives
// "this session". It never changes ScopeRedacted. The Telegram buttons, the /approve
// text, and the dashboard cards use it.
func FitScope(s Scope, noAlways, noSession bool) Scope {
	switch {
	case noSession && (s == ScopeSession || s == ScopeAlways):
		return ScopeOnce
	case noAlways && s == ScopeAlways:
		return ScopeSession
	}
	return s
}
