package sessions

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// maxForkDepth limits the walk from a session to the session that it was forked from.
const maxForkDepth = 32

// Scope is the set of sessions that one caller may read. All is true for the owner on
// the local machine. Otherwise a session is in the scope when its key, or the key of
// the session that it was forked from, is in Keys or starts with a prefix in Prefixes.
// A fork has no key, so it belongs to the scope of the nearest session with a key.
type Scope struct {
	All      bool
	Keys     []string
	Prefixes []string
}

// Has reports whether the session key is in the scope.
func (sc Scope) Has(key string) bool {
	if sc.All {
		return true
	}
	if key == "" {
		return false
	}
	if slices.Contains(sc.Keys, key) {
		return true
	}
	for _, p := range sc.Prefixes {
		if p != "" && strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// RootKey returns the key of the session id. For a fork, which has no key, it returns
// the key of the nearest session that the fork comes from. A session with no key and no
// parent gives "".
func (s *Store) RootKey(ctx context.Context, id string) (string, error) {
	cur := id
	for range maxForkDepth {
		sess, err := s.Get(ctx, cur)
		if err != nil {
			return "", err
		}
		if sess.Key != "" || sess.ParentID == "" {
			return sess.Key, nil
		}
		cur = sess.ParentID
	}
	return "", fmt.Errorf("sessions: session %s has more than %d parents", id, maxForkDepth)
}

// InScope reports whether the session id is in sc.
func (s *Store) InScope(ctx context.Context, id string, sc Scope) (bool, error) {
	if sc.All {
		return true, nil
	}
	key, err := s.RootKey(ctx, id)
	if err != nil {
		return false, err
	}
	return sc.Has(key), nil
}

// scopeFilter returns an SQL condition on the column col, a session id, and its
// arguments. The condition is true for the sessions of sc: each session with a key of
// sc, and each fork with no key below such a session. All gives "".
func scopeFilter(col string, sc Scope) (string, []any) {
	if sc.All {
		return "", nil
	}
	var conds []string
	var args []any
	if len(sc.Keys) > 0 {
		conds = append(conds, "session_key IN (?"+strings.Repeat(",?", len(sc.Keys)-1)+")")
		for _, k := range sc.Keys {
			args = append(args, k)
		}
	}
	for _, p := range sc.Prefixes {
		if p == "" {
			continue
		}
		conds = append(conds, `session_key LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(p)+"%")
	}
	if len(conds) == 0 {
		return " AND 0", nil
	}
	return " AND " + col + ` IN (WITH RECURSIVE scoped(id) AS (
		SELECT id FROM sessions WHERE session_key != '' AND (` + strings.Join(conds, " OR ") + `)
		UNION SELECT c.id FROM sessions c JOIN scoped ON c.parent_id = scoped.id AND c.session_key = '')
		SELECT id FROM scoped)`, args
}
