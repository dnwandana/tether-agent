package agent

import (
	"regexp"
	"strings"

	"github.com/dnwandana/tether-agent/internal/ctxmgr"
)

// nowPrefixRe matches the time prefix of nowPrefixFormat.
var nowPrefixRe = regexp.MustCompile(`^\[now: \d{4}-\d{2}-\d{2} \d{2}:\d{2} [+-]\d{2}\] `)

// ownerWordsWholeSkip lists the starts of user rows that hold no owner words at all.
var ownerWordsWholeSkip = []string{
	"[Background delegation ",
	ResumeNote,
	"[Goal set]",
	"[Continuing toward your standing goal",
}

// ownerWordsCuts lists the markers that start a block that the program adds to the end of a
// user row: a compaction summary, a skill command, a cron skill block, and a document.
var ownerWordsCuts = []string{
	ctxmgr.SummaryPrefix,
	`[IMPORTANT: The user has invoked the "`,
	"[Skill: ",
	"[Content of ",
}

// OwnerWords returns the words that the owner typed, without the framing blocks that the
// program adds to a user message.
func OwnerWords(text string) string {
	text = nowPrefixRe.ReplaceAllString(text, "")
	for _, p := range ownerWordsWholeSkip {
		if strings.HasPrefix(text, p) {
			return ""
		}
	}
	if i := strings.Index(text, ObservedHeader); i >= 0 {
		end := len(text)
		if j := strings.Index(text[i:], CurrentHeader); j >= 0 {
			end = i + j + len(CurrentHeader)
			if end < len(text) && text[end] == '\n' {
				end++
			}
		}
		text = text[:i] + text[end:]
	}
	text = nowPrefixRe.ReplaceAllString(text, "")
	for _, m := range ownerWordsCuts {
		if i := strings.Index(text, m); i >= 0 {
			text = text[:i]
		}
	}
	return strings.TrimSpace(text)
}
