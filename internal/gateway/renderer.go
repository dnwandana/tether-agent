package gateway

// SessionRenderer receives the id of the session of the turn before the agent starts. A
// channel uses it to find the handle values of the session for its outbound scan.
type SessionRenderer interface{ SetSession(id string) }
