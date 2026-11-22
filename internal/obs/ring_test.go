package obs

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

func ringMsgs(lines []LogLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Msg)
	}
	return out
}

func TestRingKeepsLastLines(t *testing.T) {
	ring := NewRing(3)
	log := NewLoggerRing(io.Discard, "debug", security.NewRedactor(nil), ring)
	for i := 1; i <= 5; i++ {
		log.Info(fmt.Sprintf("m%d", i))
	}
	got := ring.Lines("", "", 0)
	if !slices.Equal(ringMsgs(got), []string{"m3", "m4", "m5"}) || got[0].Level != "INFO" || got[0].Time.IsZero() {
		t.Fatalf("%+v", got)
	}
}

func TestRingFilters(t *testing.T) {
	ring := NewRing(10)
	log := NewLoggerRing(io.Discard, "debug", security.NewRedactor(nil), ring)
	log.Debug("poll telegram")
	log.Info("cron run", "job", "Nightly")
	log.Warn("slow reply", "model", "gpt")
	log.Error("cron failed", "err", "boom now")
	for _, c := range []struct {
		level, text string
		limit       int
		want        []string
	}{
		{"", "", 0, []string{"poll telegram", "cron run", "slow reply", "cron failed"}},
		{"warn", "", 0, []string{"slow reply", "cron failed"}},
		{"error", "", 0, []string{"cron failed"}},
		{"", "CRON", 0, []string{"cron run", "cron failed"}},
		{"", "nightly", 0, []string{"cron run"}},
		{"info", "cron", 1, []string{"cron failed"}},
	} {
		if got := ringMsgs(ring.Lines(c.level, c.text, c.limit)); !slices.Equal(got, c.want) {
			t.Errorf("Lines(%q, %q, %d) = %q, want %q", c.level, c.text, c.limit, got, c.want)
		}
	}
}

func TestRingAttrsText(t *testing.T) {
	ring := NewRing(10)
	log := NewLoggerRing(io.Discard, "info", security.NewRedactor(nil), ring)
	log.Info("a", "job", "nightly", "n", 3)
	log.With("part", "gateway").WithGroup("tg").Info("b", "chat", "42")
	log.Info("c", "err", "boom now")
	log.Debug("d")
	got := ring.Lines("", "", 0)
	want := []LogLine{
		{Level: "INFO", Msg: "a", Attrs: "job=nightly n=3"},
		{Level: "INFO", Msg: "b", Attrs: "part=gateway tg.chat=42"},
		{Level: "INFO", Msg: "c", Attrs: `err="boom now"`},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i].Level != want[i].Level || got[i].Msg != want[i].Msg || got[i].Attrs != want[i].Attrs {
			t.Errorf("line %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRingRedactsAndWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	ring := NewRing(10)
	log := NewLoggerRing(&buf, "info", security.NewRedactor([]string{"supersecretvalue1"}), ring)
	log.Info("token supersecretvalue1", "key", "supersecretvalue1")
	l := ring.Lines("", "", 0)
	if len(l) != 1 || strings.Contains(l[0].Msg+l[0].Attrs, "supersecretvalue1") {
		t.Fatalf("the ring holds the secret: %+v", l)
	}
	if strings.Contains(buf.String(), "supersecretvalue1") || !strings.Contains(buf.String(), `"level":"INFO"`) {
		t.Fatalf("the JSON log is wrong: %s", buf.String())
	}
}

func TestRingSubscribe(t *testing.T) {
	ring := NewRing(10)
	log := NewLoggerRing(io.Discard, "info", security.NewRedactor(nil), ring)
	var got []string
	cancel := ring.Subscribe(func(l LogLine) { got = append(got, l.Msg) })
	log.Info("one")
	cancel()
	log.Info("two")
	if !slices.Equal(got, []string{"one"}) {
		t.Fatalf("%q", got)
	}
}
