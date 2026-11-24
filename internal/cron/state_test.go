package cron

import "testing"

func TestStateOf(t *testing.T) {
	for _, c := range []struct {
		j    Job
		want string
	}{
		{Job{Enabled: false, RepeatLeft: 0}, "done"},
		{Job{Enabled: false, RepeatLeft: -1, FailStreak: 3}, "paused"},
		{Job{Enabled: true, RepeatLeft: -1, FailStreak: 1, Watch: true, LastStatus: "error"}, "failing"},
		{Job{Enabled: true, RepeatLeft: -1, Watch: true, LastStatus: "ok"}, "watch"},
		{Job{Enabled: true, RepeatLeft: -1, LastStatus: "ok"}, "ok"},
		{Job{Enabled: true, RepeatLeft: -1}, "pending"},
	} {
		if got := StateOf(c.j); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.j, got, c.want)
		}
	}
}
