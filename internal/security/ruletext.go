package security

import "strings"

// RuleText returns the readable text of the rule id of an approval_log row.
// The text has no Markdown escape. ok is false for an unknown id.
func RuleText(id string) (string, bool) {
	switch {
	case id == "":
		return "", false
	case id == CouldNotReadPatternID:
		return "could not read the command", true
	case id == ScriptPatternID:
		return "run a Python script on the host", true
	case id == "sudo_stdin":
		return "sudo password on stdin", true
	case strings.HasPrefix(id, "acp:"):
		return "coding agent permission", true
	}
	if p, ok := PatternByID(id); ok && p.Description != "" {
		return p.Description, true
	}
	for _, list := range [][]Pattern{HardlinePatterns, HumanOnlyPatterns, GatewaySelfPatterns} {
		for _, p := range list {
			if p.ID == id && p.Description != "" {
				return p.Description, true
			}
		}
	}
	protectedMu.RLock()
	defer protectedMu.RUnlock()
	for _, p := range protectedPatterns {
		if p.ID == id && p.Description != "" {
			return p.Description, true
		}
	}
	return "", false
}
