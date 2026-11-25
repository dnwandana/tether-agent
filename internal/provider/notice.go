package provider

import (
	"context"
	"time"
)

// NoticeKind is the kind of a Notice.
type NoticeKind int

const (
	// NoticeWait tells that the chain waits before it tries an entry again.
	NoticeWait NoticeKind = iota + 1
	// NoticeQuota tells that the quota of an entry is used up.
	NoticeQuota
)

// Notice is a message of the chain for the user.
type Notice struct {
	Kind NoticeKind
	// Entry and Model name the entry that failed.
	Entry, Model string
	// NextEntry and NextModel name the entry that serves the calls (NoticeQuota).
	NextEntry, NextModel string
	// Wait is the wait before the next try (NoticeWait).
	Wait time.Duration
	// Cause is the ShortCause of the error.
	Cause string
	// ResetAt is the reset time of a quota. It is zero when not known.
	ResetAt time.Time
}

type notifyKey struct{}

// WithNotify returns a context that carries fn. A nil fn clears the function.
func WithNotify(ctx context.Context, fn func(Notice)) context.Context {
	return context.WithValue(ctx, notifyKey{}, fn)
}

// notifyFrom returns the notify function of ctx, or nil when there is none.
func notifyFrom(ctx context.Context) func(Notice) {
	fn, _ := ctx.Value(notifyKey{}).(func(Notice))
	return fn
}
