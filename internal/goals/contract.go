package goals

import (
	"fmt"
	"strings"
)

// contractFields is the display order of the contract fields, the Hermes _CONTRACT_FIELDS.
// The keys are the JSON keys of Contract.
var contractFields = []string{"outcome", "verification", "constraints", "boundaries", "stop_when"}

// contractLabels is the Hermes _CONTRACT_LABELS.
var contractLabels = map[string]string{
	"outcome":      "Outcome",
	"verification": "Verification",
	"constraints":  "Constraints",
	"boundaries":   "Boundaries",
	"stop_when":    "Stop when blocked",
}

// contractAliases maps each lower-case alias that the owner can type before a value to a
// field key. It is a copy of the Hermes _CONTRACT_ALIASES.
var contractAliases = map[string]string{
	"outcome": "outcome", "goal": "outcome", "done": "outcome", "done when": "outcome",
	"verification": "verification", "verify": "verification", "verified by": "verification",
	"evidence": "verification", "proof": "verification",
	"constraints": "constraints", "constraint": "constraints", "preserve": "constraints",
	"must not": "constraints", "do not change": "constraints",
	"boundaries": "boundaries", "boundary": "boundaries", "scope": "boundaries",
	"allowed": "boundaries", "files": "boundaries",
	"stop when": "stop_when", "stop_when": "stop_when", "blocked": "stop_when",
	"stop if blocked": "stop_when", "give up when": "stop_when",
}

// field returns a pointer to the field of c with the JSON key key.
func (c *Contract) field(key string) *string {
	switch key {
	case "outcome":
		return &c.Outcome
	case "verification":
		return &c.Verification
	case "constraints":
		return &c.Constraints
	case "boundaries":
		return &c.Boundaries
	case "stop_when":
		return &c.StopWhen
	}
	return nil
}

// ParseGoalText splits the goal text into a headline and a contract from the inline
// "field: value" lines. It is a port of Hermes parse_contract. When the headline is empty,
// it returns the raw text as the headline, like goal_command.py, and the contract stays.
func ParseGoalText(text string) (headline string, c Contract) {
	var parts []string
	values := map[string][]string{}
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if prefix, value, ok := strings.Cut(line, ":"); ok {
			key, known := contractAliases[strings.ToLower(strings.TrimSpace(prefix))]
			if value = strings.TrimSpace(value); known && value != "" {
				values[key] = append(values[key], value)
				continue
			}
		}
		parts = append(parts, line)
	}
	for _, key := range contractFields {
		*c.field(key) = strings.TrimSpace(strings.Join(values[key], " "))
	}
	headline = strings.TrimSpace(strings.Join(parts, " "))
	if headline == "" {
		headline = strings.TrimSpace(text)
	}
	return headline, c
}

// Render returns the contract block: one "- <Label>: <value>" line for each field that is
// set, then one "- Extra criterion N: <text>" line for each subgoal that is not blank. It is
// a port of Hermes render_block and _render_extra_criteria. An empty contract with no
// subgoal gives "".
func (c Contract) Render(subgoals []string) string {
	var lines []string
	for _, key := range contractFields {
		if v := strings.TrimSpace(*c.field(key)); v != "" {
			lines = append(lines, "- "+contractLabels[key]+": "+v)
		}
	}
	n := 0
	for _, s := range subgoals {
		if s = strings.TrimSpace(s); s != "" {
			n++
			lines = append(lines, fmt.Sprintf("- Extra criterion %d: %s", n, s))
		}
	}
	return strings.Join(lines, "\n")
}
