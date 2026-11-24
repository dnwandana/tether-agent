package dashboard

import (
	"net/http"
	"time"
)

// CommandRow is one slash command of the command palette. Name starts with "/".
type CommandRow struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// BootstrapView is the body of GET /api/bootstrap. The page reads it once at the start.
type BootstrapView struct {
	Commands    []CommandRow `json:"commands"`
	Skills      []CommandRow `json:"skills"`
	Version     string       `json:"version"`
	BuildID     string       `json:"build_id"`
	MaxUploadMB int          `json:"max_upload_mb"`
	TLS         bool         `json:"tls"`
	DrainS      int          `json:"drain_s"`
	Toolsets    []string     `json:"toolsets"`
	// HostZone is the time zone of the host and HostOffsetMin is its offset in minutes
	// east of UTC. A cron expression runs in this zone.
	HostZone      string `json:"host_zone"`
	HostOffsetMin int    `json:"host_offset_min"`
}

func init() { registerAPI(BootstrapView{}, CommandRow{}) }

// BootstrapDeps holds what the bootstrap route reads. A nil Commands, Skills, or Toolsets
// gives an empty list. DrainS is the drain limit of a restart in seconds
// (agent.restart_drain_timeout_s). Toolsets are the toolset names for the job form.
type BootstrapDeps struct {
	Commands    func() []CommandRow
	Skills      func() []CommandRow
	Version     string
	MaxUploadMB int
	TLS         bool
	DrainS      int
	Toolsets    []string
}

type bootstrapAPI struct{ d BootstrapDeps }

// NewBootstrap returns the route GET /api/bootstrap.
func NewBootstrap(d BootstrapDeps) Routes { return &bootstrapAPI{d: d} }

// Register adds the bootstrap route to mux.
func (b *bootstrapAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/bootstrap", b.get)
}

// bootRows calls f and returns an empty slice, never nil, for a nil f or an empty result.
func bootRows(f func() []CommandRow) []CommandRow {
	if f == nil {
		return []CommandRow{}
	}
	rows := f()
	if rows == nil {
		return []CommandRow{}
	}
	return rows
}

func (b *bootstrapAPI) get(w http.ResponseWriter, _ *http.Request) {
	name, off := time.Now().Zone()
	writeJSON(w, BootstrapView{
		Commands:    bootRows(b.d.Commands),
		Skills:      bootRows(b.d.Skills),
		Version:     b.d.Version,
		BuildID:     BuildID(),
		MaxUploadMB: b.d.MaxUploadMB,
		TLS:         b.d.TLS,
		DrainS:      b.d.DrainS,
		Toolsets:    nonNil(b.d.Toolsets),

		HostZone:      name,
		HostOffsetMin: off / 60,
	})
}
