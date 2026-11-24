package cron

// StateOf returns the state of the job j: done, paused, failing, watch, ok, or
// pending. Telegram /jobs and the dashboard show a glyph for each state.
func StateOf(j Job) string {
	switch {
	case !j.Enabled && j.RepeatLeft == 0:
		return "done"
	case !j.Enabled:
		return "paused"
	case j.FailStreak > 0:
		return "failing"
	case j.Watch:
		return "watch"
	case j.LastStatus == "ok":
		return "ok"
	}
	return "pending"
}
