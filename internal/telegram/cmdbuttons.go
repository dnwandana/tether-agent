package telegram

import (
	"strings"
	"unicode/utf8"

	"github.com/dnwandana/tether-agent/internal/gateway"
)

// cmdPrefix starts the callback data of a command button: "cmd:{user}:{action}". {user} is
// the Telegram user ID of the user who sent the command that made the reply. {action} is a
// command line, "·" (no operation), or "cancel". The data holds no state in memory, so the
// buttons still work after a gateway restart.
const cmdPrefix = "cmd"

// otherUser is the callback answer to a tap on a command button of another user.
const otherUser = "⛔ This button is for another user."

// maxCallbackData is the Bot API limit of the callback data, in bytes.
const maxCallbackData = 64

// encodeCmd returns "cmd:{user}:{action}". When that is longer than 64 bytes, it cuts the end
// of the action at a UTF-8 boundary and trims a trailing space. Only the search words of a
// page command can make the data that long.
func encodeCmd(user, action string) string {
	head := cmdPrefix + ":" + user + ":"
	data := head + action
	if len(data) <= maxCallbackData {
		return data
	}
	return strings.TrimRight(cutBytes(data, maxCallbackData), " ")
}

// cutBytes returns the longest start of s that has at most n bytes and ends at a rune
// boundary.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// decodeCmd splits the callback data of a command button. The action can hold ":", for
// example "/model zai:glm-4.6", so the split uses only the first two ":". ok is false for
// data that is not a command button.
func decodeCmd(data string) (user, action string, ok bool) {
	rest, found := strings.CutPrefix(data, cmdPrefix+":")
	if !found {
		return "", "", false
	}
	user, action, found = strings.Cut(rest, ":")
	if !found || user == "" || action == "" || strings.Trim(user, "0123456789") != "" {
		return "", "", false
	}
	return user, action, true
}

// cmdKeyboard converts the button rows of a reply to an inline keyboard with command
// buttons for user. It returns nil when there are no buttons. It also returns nil when user
// is empty, because no tap can pass the user check of such a button.
func cmdKeyboard(user string, rows [][]gateway.Button) *InlineKeyboardMarkup {
	if user == "" {
		return nil
	}
	var kb *InlineKeyboardMarkup
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		r := make([]InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			r = append(r, InlineKeyboardButton{Text: b.Text, CallbackData: encodeCmd(user, b.Data)})
		}
		if kb == nil {
			kb = &InlineKeyboardMarkup{}
		}
		kb.InlineKeyboard = append(kb.InlineKeyboard, r)
	}
	return kb
}
