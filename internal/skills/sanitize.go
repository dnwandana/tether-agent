package skills

import "strings"

// The states of the escape-sequence filter in Sanitize.
const (
	sanNormal = iota // plain text
	sanEsc           // after ESC
	sanCSI           // in a CSI sequence (ESC [)
	sanOSC           // in an OSC sequence (ESC ])
	sanOSCEsc        // after ESC in an OSC sequence
)

// Sanitize returns s without terminal control characters. It removes the C0
// controls except newline and tab, DEL, and the C1 controls. It also removes
// the body of each CSI sequence and each OSC sequence, so no visible rest
// such as "[31m" stays. The CLI prints text from a skill or a remote only
// through this function, because that text can move the cursor, change the
// window title, or hide lines. The idea comes from vercel-labs/skills
// src/sanitize.ts.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	state := sanNormal
	for _, r := range s {
		switch state {
		case sanCSI:
			if r >= 0x20 && r <= 0x3f {
				continue // a parameter byte or an intermediate byte
			}
			state = sanNormal
			if r >= 0x40 && r <= 0x7e {
				continue // the final byte
			}
			// A byte that is not valid in CSI ends the sequence. Filter it as plain text.
		case sanOSC:
			switch r {
			case 0x07:
				state = sanNormal
			case 0x1b:
				state = sanOSCEsc
			}
			continue
		case sanOSCEsc:
			if r == '\\' {
				state = sanNormal
				continue
			}
			// An ESC without "\" ends the OSC and starts a new sequence.
			state = sanEsc
		}
		if state == sanEsc {
			switch r {
			case '[':
				state = sanCSI
				continue
			case ']':
				state = sanOSC
				continue
			}
			state = sanNormal
		}
		switch {
		case r == 0x1b:
			state = sanEsc
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// Drop the control character.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
