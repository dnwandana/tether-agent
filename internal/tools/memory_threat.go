package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/skills"
)

// memoryScanMaxRunes is the default entry limit of the memory store (maxEntryRunes in
// internal/memory/store.go). It is the fallback when a caller passes a limit of 0.
// The guard does not scan a longer text, because the store refuses it with the length
// error. Hermes also caps the scan (MAX_SCAN_CHARS).
const memoryScanMaxRunes = 4000

// memoryThreatRow is one rule of the memory guard: the ID, a short description,
// and the Python regex text for skills.CompilePy.
type memoryThreatRow struct{ id, desc, re string }

// memoryThreatRows are the rules of the memory guard. An entry of memory.md or
// user_profile.md goes into the system prompt of each later session, so an injected
// instruction there stays active. A note can move into tier 1 with a replace, so
// notes get the same check. The rows are a port of Hermes tools/threat_patterns.py
// (scope "strict", used by tools/memory_tool_store.py), with these changes:
//   - 11 Hermes rows are not here: c2_node_registration, c2_heartbeat, c2_task_pull,
//     c2_network_connect, forced_action, anti_forensic_oneliner, anti_forensic_disk,
//     env_var_unset_agent, known_c2_framework, c2_explicit, and c2_explicit_long.
//     They refuse ordinary facts, for example "check in with the team" and "pull
//     tasks from Jira". send_to_url and context_exfil still refuse the exfiltration
//     part of a C2 instruction.
//   - 4 rows come from the skill guard (guard_patterns.go) with the same ID and regex:
//     embedded_private_key, github_token_leaked, anthropic_key_leaked, and
//     aws_access_key_leaked. openai_key_leaked is not here, because "sk-" matches
//     ordinary words such as "task-runner".
//   - Go limits a repeat count to 1000, so "*" replaces each {0,2048}. The Go engine
//     does not backtrack, so the bound is not necessary.
//   - A \b comes before the verbs of the Hermes _MODIFY prefix, so "credit" does not
//     match "edit".
//   - hermes_env and hermes_config_mod become tether_env and tether_config_mod.
//   - Go has no lookahead. scanMemory checks the value of each hardcoded_secret match.
var memoryThreatRows = []memoryThreatRow{
	// Prompt injection.
	{"prompt_injection", "tells the agent to ignore its earlier instructions", `ignore\s+(?:\w+\s+){0,8}(previous|all|above|prior)\s+(?:\w+\s+){0,8}instructions`},
	{"sys_prompt_override", "tries to override the system prompt", `system\s+prompt\s+override`},
	{"disregard_rules", "tells the agent to disregard its instructions or rules", `disregard\s+(?:\w+\s+){0,8}(your|all|any)\s+(?:\w+\s+){0,8}(instructions|rules|guidelines)`},
	{"bypass_restrictions", "tells the agent to act as if it has no rules", `act\s+as\s+(if|though)\s+(?:\w+\s+){0,8}you\s+(?:\w+\s+){0,8}(have\s+no|don't\s+have)\s+(?:\w+\s+){0,8}(restrictions|limits|rules)`},
	{"html_comment_injection", "hides an instruction in an HTML comment", `<!--[^>]{0,512}(?:ignore|override|system|secret|hidden)[^>]{0,512}-->`},
	{"hidden_div", "hides text in an HTML block that does not show", `<\s*div\s+style\s*=\s*["'][^>]*display\s*:\s*none`},
	{"translate_execute", "tells the agent to translate a text and then run it", `translate\s+[^\n]{0,512}\s+into\s+\w+(?:[\s-]+\w+){0,2}\s+and\s+(execute|run|eval)\b`},
	{"deception_hide", "tells the agent to hide something from the user", `do\s+not\s+(?:\w+\s+){0,8}tell\s+(?:\w+\s+){0,8}the\s+user`},
	// Role and identity hijack.
	{"role_hijack", "gives the agent a new identity", `you\s+are\s+(?:\w+\s+){0,8}now\s+(?:a|an|the)\s+`},
	{"role_pretend", "tells the agent to pretend to be something else", `pretend\s+(?:\w+\s+){0,8}(you\s+are|to\s+be)\s+`},
	{"leak_system_prompt", "tells the agent to show its system prompt", `output\s+(?:\w+\s+){0,8}(system|initial)\s+prompt`},
	{"remove_filters", "tells the agent to answer without its safety rules", `(respond|answer|reply)\s+without\s+(?:\w+\s+){0,8}(restrictions|limitations|filters|safety)`},
	{"fake_update", "says that the agent got an update", `you\s+have\s+been\s+(?:\w+\s+){0,8}(updated|upgraded|patched)\s+to`},
	{"identity_override", "tells the agent to give itself a new name", `\bname\s+yourself\s+\w+`},
	// Exfiltration.
	{"exfil_curl", "sends a secret variable with curl", `curl\s+[^\n]*\$\{?\w*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)S?\b`},
	{"exfil_wget", "sends a secret variable with wget", `wget\s+[^\n]*\$\{?\w*(?:KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL)S?\b`},
	{"read_secrets", "reads a file that holds secrets", `cat\s+[^\n]*(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`},
	{"send_to_url", "tells the agent to send data to a URL", `(send|post|upload|transmit)\s+[^\n]*\s+(to|at)\s+https?://`},
	{"context_exfil", "tells the agent to share the conversation", `(include|output|print|share)\s+(?:\w+\s+){0,8}(conversation|chat\s+history|previous\s+messages|full\s+context|entire\s+context)`},
	// Persistence and agent config.
	{"ssh_backdoor", "names the SSH authorized_keys file", `authorized_keys`},
	{"ssh_access", "writes to the SSH folder", `(?:\b(?:echo|cat|cp|mv|dd|tee|install|printf|rsync|scp|ln|append|add|write|sed|chmod|chown|truncate|rm|touch|curl|wget|git)\b|\bopen\s*\(|>>?)[^\n]{0,512}(?:\$HOME/\.ssh|~/\.ssh)`},
	{"tether_env", "names the secrets file of Tether Agent", `\$HOME/\.tether/\.env|~/\.tether/\.env`},
	{"agent_config_mod", "tells the agent to change an agent instruction file", `\b(update|modify|edit|write|change|append|add\s+to)\s+[^\n]*(?:AGENTS\.md|CLAUDE\.md|\.cursorrules|\.clinerules)`},
	{"tether_config_mod", "tells the agent to change the config of Tether Agent", `\b(update|modify|edit|write|change|append|add\s+to)\s+[^\n]*\.tether/config\.yaml`},
	// Secrets. The capture group of hardcoded_secret holds the value.
	{"hardcoded_secret", "holds a secret value", `(?:api[_-]?key|token|secret|password)\s*[=:]\s*["']([A-Za-z0-9+/=_-]{20,})`},
	{"embedded_private_key", "holds a private key", `-----BEGIN\s+(RSA\s+)?PRIVATE\s+KEY-----`},
	{"github_token_leaked", "holds a GitHub token", `ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{80,}`},
	{"anthropic_key_leaked", "holds an Anthropic API key", `sk-ant-[A-Za-z0-9_-]{90,}`},
	{"aws_access_key_leaked", "holds an AWS access key ID", `AKIA[0-9A-Z]{16}`},
}

// memoryThreatPatterns compiles memoryThreatRows on the first scan, in the same order.
var memoryThreatPatterns = sync.OnceValue(func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(memoryThreatRows))
	for i, r := range memoryThreatRows {
		out[i] = skills.CompilePy(r.re)
	}
	return out
})

// envVarName matches an environment variable name, with case. A hardcoded_secret
// value that is such a name, followed by a quote, says where a secret lives.
var envVarName = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+$`)

// memoryFinding is one finding of the memory guard.
type memoryFinding struct{ ID, Description string }

// scanMemory returns the findings of the memory guard for content, in order. A
// maxRunes of 0 or less uses memoryScanMaxRunes.
func scanMemory(content string, maxRunes int) []memoryFinding {
	if maxRunes <= 0 {
		maxRunes = memoryScanMaxRunes
	}
	// The store trims the text before its length check and saves the trimmed text. Trim
	// here too, so spaces at the end cannot put a text over the scan limit.
	content = strings.TrimSpace(content)
	if utf8.RuneCountInString(content) > maxRunes {
		return nil
	}
	var out []memoryFinding
	// Check the raw text, because NFKC can change these runes.
	for _, r := range content {
		if _, name, ok := skills.InvisibleRune(string(r)); ok {
			out = append(out, memoryFinding{"invisible_unicode", name})
			break
		}
		if r >= 0xE0000 && r <= 0xE007F {
			out = append(out, memoryFinding{"invisible_unicode", fmt.Sprintf("tag character U+%04X", r)})
			break
		}
	}
	// Hermes scan_for_threats applies each row to the whole text, not to one line.
	text := skills.FoldPythonCase(security.FoldLookalikes(norm.NFKC.String(content)))
	for i, re := range memoryThreatPatterns() {
		row := memoryThreatRows[i]
		if row.id == "hardcoded_secret" {
			if !realSecret(re, text) {
				continue
			}
		} else if !re.MatchString(text) {
			continue
		}
		out = append(out, memoryFinding{row.id, row.desc})
	}
	return out
}

// realSecret reports whether one hardcoded_secret match of text holds a value
// that is not the name of an environment variable. It replaces the Hermes
// lookahead. It checks every match, so a decoy name before a real secret does
// not hide the secret.
func realSecret(re *regexp.Regexp, text string) bool {
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2], m[3]
		if envVarName.MatchString(text[start:end]) && end < len(text) && (text[end] == '"' || text[end] == '\'') {
			continue
		}
		return true
	}
	return false
}

// memoryThreat returns the refusal text for a memory write, or "" when the text is allowed.
// A maxRunes of 0 or less uses memoryScanMaxRunes.
func memoryThreat(content string, maxRunes int) string {
	findings := scanMemory(content, maxRunes)
	if len(findings) == 0 {
		return ""
	}
	ids := make([]string, len(findings))
	for i, f := range findings {
		ids[i] = fmt.Sprintf("%s (%s)", f.ID, f.Description)
	}
	return "the memory guard refused the entry: " + strings.Join(ids, "; ") + ". Memory goes into the " +
		"system prompt of later sessions, so it must not hold instructions to the agent or commands that " +
		"send data out. Save the fact in your own words, without the flagged text."
}

// MemoryThreatIDs returns the finding IDs of the memory threat scan for text, with
// the default limit of 4000 runes.
func MemoryThreatIDs(text string) []string { return MemoryThreatIDsWithin(text, 0) }

// MemoryThreatIDsWithin returns the finding IDs of the memory threat scan for text. A
// text of more than maxRunes runes gives "too_long". A maxRunes of 0 or less uses
// memoryScanMaxRunes. It returns nil when there is no finding.
func MemoryThreatIDsWithin(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = memoryScanMaxRunes
	}
	if utf8.RuneCountInString(strings.TrimSpace(text)) > maxRunes {
		return []string{"too_long"}
	}
	var ids []string
	for _, f := range scanMemory(text, maxRunes) {
		ids = append(ids, f.ID)
	}
	return ids
}

// MemoryEntryID returns the short ID of a memory entry: "m-" and the first 8 hex
// characters of the SHA-256 of the raw entry.
func MemoryEntryID(entry string) string {
	sum := sha256.Sum256([]byte(entry))
	return "m-" + hex.EncodeToString(sum[:])[:8]
}

// MemoryBlockedLine returns the line that replaces a blocked entry in the system
// prompt, with the default limit of 4000 runes.
func MemoryBlockedLine(entry string) (line string, blocked bool) {
	return MemoryBlockedLineWithin(entry, 0)
}

// MemoryBlockedLineWithin returns the line that replaces a blocked entry in the system
// prompt. It returns false and no line when the scan has no finding. A maxRunes of 0 or
// less uses memoryScanMaxRunes.
func MemoryBlockedLineWithin(entry string, maxRunes int) (line string, blocked bool) {
	ids := MemoryThreatIDsWithin(entry, maxRunes)
	if len(ids) == 0 {
		return "", false
	}
	id := MemoryEntryID(entry)
	return "[BLOCKED " + id + ": this entry matched the memory threat scan (" + strings.Join(ids, ", ") +
		"). Remove it with the memory tool: remove " + id + ".]", true
}
