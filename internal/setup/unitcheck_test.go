package setup

import (
	"reflect"
	"testing"
)

func TestUnitProblemsCurrentUnit(t *testing.T) {
	fixedTZ(t, "UTC")
	if p := UnitProblems(UnitText("/bin/tether", "/d", 180), 180); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestUnitProblemsOldUnit(t *testing.T) {
	old := "[Service]\nExecStart=/bin/tether gateway\nRestart=on-failure\nRestartSec=5\n"
	p := UnitProblems(old, 180)
	want := []string{
		"The unit has no TimeoutStopSec, so systemd stops the gateway after 90 s. The drain is 180 s.",
		`The unit has no ExecReload, so "systemctl reload tether" does not restart the gateway.`,
		"The unit has no RestartForceExitStatus=75 and SuccessExitStatus=75, so a /restart shows as a failure in the journal.",
		"The unit has no KillMode=mixed, so systemd stops the running tool commands at the start of the drain.",
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%q", p)
	}
}

func TestUnitProblemsTimeSpans(t *testing.T) {
	base := "[Service]\nExecReload=/bin/kill -USR1 $MAINPID\nRestartForceExitStatus=75\nSuccessExitStatus=75\nKillMode=mixed\n"
	for span, short := range map[string]bool{
		"210": false, "210s": false, "3min 30s": false, "4min": false, "1h": false, "infinity": false,
		"0": false, "0s": false, "-5": false, "-5s": false,
		"90": true, "3min": true, "209s": true,
	} {
		p := UnitProblems(base+"TimeoutStopSec="+span+"\n", 180)
		if (len(p) == 1) != short {
			t.Fatalf("%s: %q", span, p)
		}
		if short && p[0] != "The unit has TimeoutStopSec="+span+", which is shorter than the drain of 180 s plus 30 s." {
			t.Fatalf("%q", p[0])
		}
	}
}

func TestUnitProblemsParser(t *testing.T) {
	text := "[Unit]\nTimeoutStopSec=1\n[Service]\n# TimeoutStopSec=1\nTimeoutStopSec=1\nTimeoutStopSec=300\n" +
		"ExecReload=/bin/true\nRestartForceExitStatus=75\nSuccessExitStatus=75\nKillMode=mixed\n[Install]\nExecReload=\n"
	if p := UnitProblems(text, 180); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestUnitProblemsKillMode(t *testing.T) {
	base := "[Service]\nExecReload=/bin/kill -USR1 $MAINPID\nRestartForceExitStatus=75\nSuccessExitStatus=75\nTimeoutStopSec=210\n"
	want := "The unit has no KillMode=mixed, so systemd stops the running tool commands at the start of the drain."
	for line, warn := range map[string]bool{
		"": true, "KillMode=control-group\n": true, "KillMode=process\n": true, "KillMode=mixed\n": false,
	} {
		p := UnitProblems(base+line, 180)
		if warn && !reflect.DeepEqual(p, []string{want}) || !warn && len(p) != 0 {
			t.Fatalf("%q: %q", line, p)
		}
	}
}

func TestUnitNext(t *testing.T) {
	want := `run "tether setup" and answer yes at "Write the systemd unit?". Then run "systemctl --user daemon-reload".`
	if got := UnitNext(false); got != want {
		t.Fatalf("%q", got)
	}
	if got := UnitNext(true); got == want || got == "" {
		t.Fatalf("%q", got)
	}
}
