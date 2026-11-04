package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/dnwandana/tether-agent/internal/cron"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// callerScope returns the sessions that the caller of a tool may read. The owner on the
// local machine (the CLI, and a cron job from the CLI) reads all sessions. A Telegram
// caller reads only its own chat: the sessions with its session key, the forks of
// those sessions, and the runs of its own cron jobs. A DM also includes the topics of
// that DM. A group with one session for each user gives each user only their own key.
//
// The key comes from the session row and its parents, because a fork has no key. A
// cron run gets the key of the chat that created the job. An error never gives the
// full scope.
func callerScope(ctx context.Context, store *sessions.Store, jobs *cron.Store, env *Env) (sessions.Scope, error) {
	if env == nil {
		return sessions.Scope{All: true}, nil
	}
	key := env.SessionKey
	if store != nil && env.SessionID != "" {
		k, err := store.RootKey(ctx, env.SessionID)
		if err != nil {
			return sessions.Scope{}, fmt.Errorf("cannot find the session of the call: %w", err)
		}
		key = k
	}
	var sc sessions.Scope
	if id, ok := strings.CutPrefix(key, "cron:"); ok {
		sc.Keys = append(sc.Keys, key)
		if jobs == nil {
			return sc, nil
		}
		j, err := jobs.Get(ctx, id)
		if err != nil {
			// The job is gone, so only its own runs are in the scope.
			return sc, nil
		}
		key = j.Origin.SessionKey
	}
	if !strings.HasPrefix(key, "telegram:") {
		if key == "" && env.Channel == "telegram" {
			// A Telegram run with no key: fail closed.
			return sc, nil
		}
		return sessions.Scope{All: true}, nil
	}
	sc.Keys = append(sc.Keys, key)
	if parts := strings.Split(key, ":"); len(parts) >= 3 && parts[1] == "dm" {
		dm := strings.Join(parts[:3], ":")
		sc.Keys = append(sc.Keys, dm)
		sc.Prefixes = append(sc.Prefixes, dm+":")
	}
	if jobs != nil {
		all, err := jobs.List(ctx)
		if err != nil {
			return sessions.Scope{}, fmt.Errorf("cannot list the cron jobs: %w", err)
		}
		for _, j := range all {
			if sc.Has(j.Origin.SessionKey) {
				sc.Keys = append(sc.Keys, "cron:"+j.ID)
			}
		}
	}
	return sc, nil
}
