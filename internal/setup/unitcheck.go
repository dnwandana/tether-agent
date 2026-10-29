package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultStopTimeoutS is the stop timeout that systemd uses when the unit sets none.
const defaultStopTimeoutS = 90

// UnitProblems returns one sentence for each part of the unit text that is too old for the
// restart. The order is the stop timeout, ExecReload, exit status 75, and KillMode. The
// result is empty for a current unit. drainS is the drain time in seconds.
func UnitProblems(text string, drainS int) []string {
	svc := serviceKeys(text)
	var out []string
	need := StopTimeoutS(drainS)
	if v, ok := svc["TimeoutStopSec"]; !ok {
		if need > defaultStopTimeoutS {
			out = append(out, fmt.Sprintf("The unit has no TimeoutStopSec, so systemd stops the gateway after %d s. The drain is %d s.",
				defaultStopTimeoutS, drainS))
		}
	} else if secs, finite, valid := parseTimeSpan(v); valid && finite && secs < need {
		out = append(out, fmt.Sprintf("The unit has TimeoutStopSec=%s, which is shorter than the drain of %d s plus 30 s.", v, drainS))
	}
	if svc["ExecReload"] == "" {
		out = append(out, `The unit has no ExecReload, so "systemctl reload tether" does not restart the gateway.`)
	}
	if !hasStatus75(svc["RestartForceExitStatus"]) || !hasStatus75(svc["SuccessExitStatus"]) {
		out = append(out, "The unit has no RestartForceExitStatus=75 and SuccessExitStatus=75, so a /restart shows as a failure in the journal.")
	}
	if svc["KillMode"] != "mixed" {
		out = append(out, "The unit has no KillMode=mixed, so systemd stops the running tool commands at the start of the drain.")
	}
	return out
}

// FindUnit returns the first unit file that exists. Nil paths use the user unit path, then
// the system unit path.
func FindUnit(paths []string) (path, text string, ok bool) {
	if paths == nil {
		for _, system := range []bool{false, true} {
			if p, err := unitPath(system); err == nil {
				paths = append(paths, p)
			}
		}
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err == nil {
			return p, string(b), true
		}
	}
	return "", "", false
}

// UnitNext returns the action that replaces an old unit.
func UnitNext(system bool) string {
	reload := `"systemctl --user daemon-reload"`
	if system {
		reload = `"sudo systemctl daemon-reload"`
	}
	return `run "tether setup" and answer yes at "Write the systemd unit?". Then run ` + reload + "."
}

// checkUnit checks the installed unit. It returns false when no unit exists.
func (d *Doctor) checkUnit(drainS int) (Check, bool) {
	path, text, ok := FindUnit(d.UnitPaths)
	if !ok {
		return Check{}, false
	}
	c := Check{Name: "systemd unit", Status: StatusOK, Detail: path + " is current"}
	if problems := UnitProblems(text, drainS); len(problems) > 0 {
		system := filepath.Dir(path) == systemUnitDir
		c.Status, c.Detail, c.Next = StatusWarn, strings.Join(problems, " "), UnitNext(system)
	}
	return c, true
}

// serviceKeys returns the Key=Value pairs of the [Service] section. The last value wins.
func serviceKeys(text string) map[string]string {
	out := map[string]string{}
	inService := false
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
		case line[0] == '[':
			inService = line == "[Service]"
		case inService:
			if k, v, found := strings.Cut(line, "="); found {
				out[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
	}
	return out
}

// hasStatus75 reports whether a list of exit statuses holds 75.
func hasStatus75(v string) bool {
	for _, f := range strings.Fields(v) {
		if f == "75" {
			return true
		}
	}
	return false
}

// parseTimeSpan parses a systemd time span in seconds. The value is a plain number or a sum
// of parts with the units s, sec, m, min, and h. finite is false for "infinity" and for 0,
// because systemd disables the timeout for both. valid is false for a negative number and
// for a value that this parser does not know.
func parseTimeSpan(v string) (secs int, finite, valid bool) {
	v = strings.TrimSpace(v)
	if v == "infinity" {
		return 0, false, true
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return 0, false, false
		}
		return n, n != 0, true
	}
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return 0, false, false
	}
	total := 0
	for _, f := range fields {
		i := 0
		for i < len(f) && f[i] >= '0' && f[i] <= '9' {
			i++
		}
		n, err := strconv.Atoi(f[:i])
		if err != nil {
			return 0, false, false
		}
		switch f[i:] {
		case "s", "sec":
			total += n
		case "m", "min":
			total += n * 60
		case "h":
			total += n * 3600
		default:
			return 0, false, false
		}
	}
	return total, total != 0, true
}
