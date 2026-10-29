package obs

import (
	"fmt"
	"time"
)

// Ago returns the age d as text, for example "5 min ago", "3 h ago", or "2 days ago". It
// rounds down to whole units. A negative age, for example after a clock change, gives
// "less than 1 min ago".
//
// The gateway has its own formatAge for the session list. That text is not correct for an
// outage: it has no hours, and "today" is too vague for the age of a failure.
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than 1 min ago"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	if days := int(d / (24 * time.Hour)); days > 1 {
		return fmt.Sprintf("%d days ago", days)
	}
	return "1 day ago"
}
