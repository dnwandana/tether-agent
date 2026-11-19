package tools

import (
	"encoding/json"
	"maps"
	"net/url"
	"sort"
	"strings"

	"github.com/dnwandana/tether-agent/internal/security"
)

// outsideKey is the Env.Values key of the outside set of the run.
const outsideKey = "outside"

// maxOutsideHosts is the number of hosts that the set keeps.
const maxOutsideHosts = 20

// OutsideFacts are the facts of a run that read outside content: the sources, the hosts of
// the sources, and a count of each tool call. They hold no content.
type OutsideFacts struct {
	Sources, Hosts []string
	ToolCounts     map[string]int
}

// outsideSet is the set in Env.Values. valuesMu guards it.
type outsideSet struct {
	sources, hosts map[string]bool
	counts         map[string]int
}

// setLocked returns the set of env. When create is true, it makes the set and the map.
// The caller holds valuesMu.
func setLocked(env *Env, create bool) *outsideSet {
	if env == nil {
		return nil
	}
	if s, ok := env.Values[outsideKey].(*outsideSet); ok {
		return s
	}
	if !create {
		return nil
	}
	if env.Values == nil {
		env.Values = map[string]any{}
	}
	s := &outsideSet{sources: map[string]bool{}, hosts: map[string]bool{}, counts: map[string]int{}}
	env.Values[outsideKey] = s
	return s
}

func (s *outsideSet) addHost(host string) {
	h := validHost(host)
	if h == "" {
		return
	}
	if s.hosts[h] || len(s.hosts) < maxOutsideHosts {
		s.hosts[h] = true
	}
}

// validHost returns the lower-case host, or "" when it is not 1 to 253 characters of [a-z0-9.-].
func validHost(h string) string {
	h = strings.ToLower(h)
	if h == "" || len(h) > 253 {
		return ""
	}
	for _, r := range h {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return ""
		}
	}
	return h
}

// MarkOutside adds source, and host when it is valid, to the outside set of the run. An
// empty source does nothing.
func MarkOutside(env *Env, source, host string) {
	if env == nil || source == "" {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	s := setLocked(env, true)
	s.sources[source] = true
	s.addHost(host)
}

// Outside returns a copy of the outside facts of the run. A nil env gives the zero value.
func Outside(env *Env) OutsideFacts {
	valuesMu.Lock()
	defer valuesMu.Unlock()
	s := setLocked(env, false)
	if s == nil {
		return OutsideFacts{}
	}
	var f OutsideFacts
	for k := range s.sources {
		f.Sources = append(f.Sources, k)
	}
	for k := range s.hosts {
		f.Hosts = append(f.Hosts, k)
	}
	sort.Strings(f.Sources)
	sort.Strings(f.Hosts)
	if len(s.counts) > 0 {
		f.ToolCounts = make(map[string]int, len(s.counts))
		maps.Copy(f.ToolCounts, s.counts)
	}
	return f
}

// ResetOutside empties the outside set of the run.
func ResetOutside(env *Env) {
	if env == nil {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	delete(env.Values, outsideKey)
}

// AddOutside adds each source, host, and count of f to the outside set of the run.
func AddOutside(env *Env, f OutsideFacts) {
	if env == nil || (len(f.Sources) == 0 && len(f.Hosts) == 0 && len(f.ToolCounts) == 0) {
		return
	}
	valuesMu.Lock()
	defer valuesMu.Unlock()
	s := setLocked(env, true)
	for _, k := range f.Sources {
		if k != "" {
			s.sources[k] = true
		}
	}
	for _, h := range f.Hosts {
		s.addHost(h)
	}
	for k, v := range f.ToolCounts {
		s.counts[k] += v
	}
}

// IsOutsideTool reports whether the tool returns text from outside the machine.
func IsOutsideTool(name string) bool {
	return name == "web_fetch" || name == "web_search" ||
		strings.HasPrefix(name, "browser_") || strings.HasPrefix(name, "mcp_")
}

// countTool adds one to the call count of the tool.
func countTool(env *Env, name string) {
	valuesMu.Lock()
	defer valuesMu.Unlock()
	setLocked(env, true).counts[name]++
}

// noteOutside marks the run after a call of an outside tool. A terminal call marks the run
// only when it had no error and a part of the command is a network program.
func noteOutside(name string, t Tool, args json.RawMessage, env *Env, runErr error) {
	switch {
	case name == "terminal":
		if runErr != nil {
			return
		}
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) != nil {
			return
		}
		for _, c := range security.ReadCommand(a.Command).Cmds {
			if prog, host, ok := security.IsNetworkProgram(c); ok {
				MarkOutside(env, "terminal:"+prog, host)
			}
		}
	case name == "web_fetch" || name == "browser_navigate":
		var a struct {
			URL string `json:"url"`
		}
		host := ""
		if json.Unmarshal(args, &a) == nil {
			if u, err := url.Parse(a.URL); err == nil {
				host = u.Hostname()
			}
		}
		MarkOutside(env, name, host)
	case strings.HasPrefix(name, "mcp_"):
		MarkOutside(env, name, strings.TrimPrefix(t.Toolset(), "mcp_"))
	case IsOutsideTool(name):
		MarkOutside(env, name, "")
	}
}
