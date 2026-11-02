package skills

import (
	"regexp"
	"strings"
)

// CommandSlug returns the slash command name of the skill name. It follows the
// Hermes slug, limited to ASCII. An empty result means that the skill gets no
// command.
func CommandSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r == ' ' || r == '_' {
			r = '-'
		}
		switch {
		case r == '-':
			// Write a dash only before the next kept character, so that runs
			// collapse and no dash stays at the start or at the end.
			dash = b.Len() > 0
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if dash {
				b.WriteByte('-')
				dash = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// templateVarRe matches the Hermes template tokens of a skill body.
var templateVarRe = regexp.MustCompile(`\$\{(HERMES_SKILL_DIR|HERMES_SESSION_ID)\}`)

// SubstituteTemplateVars replaces ${HERMES_SKILL_DIR} with dir and
// ${HERMES_SESSION_ID} with sessionID in body. A token with an empty value
// stays as it is (Hermes agent/skill_preprocessing.py).
func SubstituteTemplateVars(body, dir, sessionID string) string {
	return templateVarRe.ReplaceAllStringFunc(body, func(tok string) string {
		val := dir
		if tok == "${HERMES_SESSION_ID}" {
			val = sessionID
		}
		if val == "" {
			return tok
		}
		return val
	})
}
