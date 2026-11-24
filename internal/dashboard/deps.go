package dashboard

import (
	"context"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/sessions"
)

// Deps is what the channel needs from the app. app builds it in AddDashboard. The function
// fields are the parts that live in app or in cmd/tether, so this package does not import them.
type Deps struct {
	Cfg         config.DashboardConfig
	DB          *db.DB
	Sessions    *sessions.Store
	Redact      func(string) string
	Partial     func(string) int
	Log         *slog.Logger
	Doctor      func(ctx context.Context) ([]DoctorCheck, time.Time, error)
	UpdateCheck func(ctx context.Context) (UpdateInfo, error)
	UpdateRun   func(ctx context.Context) error
	Pairing     PairingFuncs
	SetupState  func(ctx context.Context) []SetupStep
	SaveSecret  func(name, value string) error
	Status      func(ctx context.Context) Status
	TurnCost    func(ctx context.Context, sessionID string, since time.Time) (CostView, error)
	Routes      []Routes
	// Session gives the header data for the hello event. It can be nil.
	Session func(ctx context.Context) SessionView
	// HeldResolve resolves a held job by id and action. It can be nil.
	HeldResolve func(ctx context.Context, id, action string) (string, error)
}

// Routes adds the routes of one page to the API mux. Each route has the checks of Auth.
type Routes interface{ Register(mux *http.ServeMux) }

// DoctorCheck is one line of the doctor. Status is ok, warn, or fail.
type DoctorCheck struct {
	Status string `json:"status"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Next   string `json:"next"`
}

// UpdateInfo is the result of an update check. Writable is false when this user cannot
// replace the binary at Path.
type UpdateInfo struct {
	Current  string `json:"current"`
	Latest   string `json:"latest"`
	Newer    bool   `json:"newer"`
	NotesURL string `json:"notes_url"`
	Writable bool   `json:"writable"`
	Path     string `json:"path"`
	// CheckedAt is the time of the GitHub check. The operations route keeps a result for 10 min.
	CheckedAt time.Time `json:"checked_at"`
}

// PairingFuncs are the pairing actions of telegram.Pairing. cmd/tether gives them.
type PairingFuncs struct {
	List    func(ctx context.Context) ([]PairingRow, error)
	Approve func(ctx context.Context, code string) error
	Revoke  func(ctx context.Context, platform, userID string) error
	// TriesLeft returns the failed approvals left before the lockout. It can be nil.
	TriesLeft func(ctx context.Context) (int, error)
	// ListRevoked returns the revoked people. It can be nil.
	ListRevoked func(ctx context.Context) ([]PairingRow, error)
	// LockedUntil returns the end of the lockout, or the zero time. It can be nil.
	LockedUntil func(ctx context.Context) (time.Time, error)
}

// PairingRow is one pending request, one approved user, or one revoked user. Status is pending, approved, or revoked.
type PairingRow struct {
	Platform string    `json:"platform"`
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Since    time.Time `json:"since"`
	Expires  time.Time `json:"expires"`
}

// SetupState is the state of one Getting started step.
type SetupState string

// The states of a setup step. Unknown means that the step needs a doctor result.
const (
	SetupDone    SetupState = "done"
	SetupOpen    SetupState = "open"
	SetupUnknown SetupState = "unknown"
)

// SetupStep is one step of "Getting started". State is done, open, or unknown. Command is the
// command that does the step.
type SetupStep struct {
	Name    string     `json:"name"`
	State   SetupState `json:"state"`
	Detail  string     `json:"detail"`
	Command string     `json:"command"`
}

func init() {
	registerAPI(DoctorCheck{}, UpdateInfo{}, PairingRow{}, SetupStep{})
	registerEnum(reflect.TypeFor[SetupState](), string(SetupDone), string(SetupOpen), string(SetupUnknown))
}
