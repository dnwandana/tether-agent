package obs

import (
	"testing"
	"time"
)

func TestAgo(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Minute, "less than 1 min ago"},
		{0, "less than 1 min ago"},
		{59 * time.Second, "less than 1 min ago"},
		{time.Minute, "1 min ago"},
		{2*time.Minute + 30*time.Second, "2 min ago"},
		{59 * time.Minute, "59 min ago"},
		{time.Hour, "1 h ago"},
		{23*time.Hour + 59*time.Minute, "23 h ago"},
		{24 * time.Hour, "1 day ago"},
		{47 * time.Hour, "1 day ago"},
		{48 * time.Hour, "2 days ago"},
	}
	for _, c := range cases {
		if got := Ago(c.d); got != c.want {
			t.Errorf("Ago(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
