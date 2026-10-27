package tools

import (
	"sort"
	"strings"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// The names of the built-in toolsets. An MCP tool carries the set
// mcp_<server>.
const (
	SetTerminal      = "terminal"
	SetFile          = "file"
	SetWeb           = "web"
	SetBrowser       = "browser"
	SetVision        = "vision"
	SetAsk           = "ask"
	SetMemory        = "memory"
	SetSkills        = "skills"
	SetCron          = "cron"
	SetDelegation    = "delegation"
	SetCodeExecution = "code_execution"
	SetMedia         = "media"
	SetMCP           = "mcp"
)

// SetAll is the name in a toolset list that means every toolset.
const SetAll = "all"

// mcpPrefix starts the set name of the tools of one MCP server.
const mcpPrefix = SetMCP + "_"

// AllToolsets returns the names of the named toolsets, MCP included.
func AllToolsets() []string {
	return []string{
		SetTerminal, SetFile, SetWeb, SetBrowser, SetVision, SetAsk, SetMemory,
		SetSkills, SetCron, SetDelegation, SetCodeExecution, SetMedia, SetMCP,
	}
}

// ValidToolset reports whether name is a toolset name: a named toolset,
// "all", or mcp_<server> for a server in mcpServers.
func ValidToolset(name string, mcpServers []string) bool {
	if name == SetAll {
		return true
	}
	for _, s := range AllToolsets() {
		if name == s {
			return true
		}
	}
	if server, ok := strings.CutPrefix(name, mcpPrefix); ok {
		for _, s := range mcpServers {
			if s != "" && s == server {
				return true
			}
		}
	}
	return false
}

// covers reports whether the set list sets allows the toolset name. The
// set "mcp" allows each mcp_<server> set.
func covers(sets map[string]bool, name string) bool {
	return sets[name] || (strings.HasPrefix(name, mcpPrefix) && sets[SetMCP])
}

// expand returns the list as a set, with "all" replaced by every toolset.
func expand(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, n := range list {
		if n == SetAll {
			for _, s := range AllToolsets() {
				out[s] = true
			}
			continue
		}
		if n != "" {
			out[n] = true
		}
	}
	return out
}

// Resolve returns the allowed toolsets. The channel list may contain "all".
// A non-empty narrow list keeps only the sets that both lists allow, so it can
// never add a set. The sets in remove are taken out last.
func Resolve(channel []string, narrow []string, remove []string) map[string]bool {
	out := expand(channel)
	if len(narrow) > 0 {
		n := expand(narrow)
		both := map[string]bool{}
		for name := range n {
			if covers(out, name) {
				both[name] = true
			}
		}
		for name := range out {
			if covers(n, name) {
				both[name] = true
			}
		}
		out = both
	}
	for _, name := range remove {
		if name == SetAll {
			return map[string]bool{}
		}
		delete(out, name)
		if name == SetMCP {
			for n := range out {
				if strings.HasPrefix(n, mcpPrefix) {
					delete(out, n)
				}
			}
		}
	}
	return out
}

// Visible returns the names and the schemas of the tools whose toolset is in
// sets, without the tools in removeTools. The set "mcp" makes the tools of
// every MCP server visible. The schemas are sorted by name, so the order
// stays stable for the prompt cache.
func (r *Registry) Visible(sets map[string]bool, removeTools []string) (names map[string]bool, schemas []provider.ToolSchema) {
	removed := make(map[string]bool, len(removeTools))
	for _, n := range removeTools {
		removed[n] = true
	}
	names = map[string]bool{}
	for _, t := range r.All() {
		if removed[t.Name()] || !covers(sets, t.Toolset()) {
			continue
		}
		names[t.Name()] = true
		schemas = append(schemas, t.Schema())
	}
	sort.SliceStable(schemas, func(i, j int) bool { return schemas[i].Name < schemas[j].Name })
	return names, schemas
}
