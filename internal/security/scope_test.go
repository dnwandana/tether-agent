package security

import "testing"

func TestFitScope(t *testing.T) {
	for _, c := range []struct {
		in                  Scope
		noAlways, noSession bool
		want                Scope
	}{
		{ScopeAlways, false, false, ScopeAlways},
		{ScopeAlways, true, false, ScopeSession},
		{ScopeSession, true, false, ScopeSession},
		{ScopeSession, true, true, ScopeOnce},
		{ScopeAlways, true, true, ScopeOnce},
		{ScopeSession, false, true, ScopeOnce},
		{ScopeDeny, true, true, ScopeDeny},
		{ScopeOnce, true, true, ScopeOnce},
		{ScopeRedacted, true, true, ScopeRedacted},
		{ScopeTimeout, true, true, ScopeTimeout},
	} {
		if got := FitScope(c.in, c.noAlways, c.noSession); got != c.want {
			t.Errorf("FitScope(%v, %v, %v) = %v, want %v", c.in, c.noAlways, c.noSession, got, c.want)
		}
	}
}
