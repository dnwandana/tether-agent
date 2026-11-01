package goals

import "testing"

func TestCheckGateCommand(t *testing.T) {
	cases := []struct {
		cmd                string
		inGateway, sudoSet bool
		want               GateCheck
	}{
		{"go test ./...", true, false, GateCheck{}},
		{"rm -rf /", true, false, GateCheck{Refused: true,
			Reason:      "BLOCKED (hardline): recursive delete of root filesystem. This command cannot be a gate.",
			Description: "recursive delete of root filesystem"}},
		{"systemctl restart tether", true, false, GateCheck{Refused: true,
			Reason:      "BLOCKED: stop/restart own gateway via systemctl. This command cannot be a gate.",
			Description: "stop/restart own gateway via systemctl"}},
		{"systemctl restart tether", false, false, GateCheck{Dangerous: true,
			Reason:      "⚠️ This gate matches a dangerous pattern: stop/restart system service. The gate runs before each judge call, with no approval prompt. Add it?",
			Description: "stop/restart system service"}},
		{"echo pw | sudo -S make install", true, false, GateCheck{Refused: true,
			Reason: "sudo with a password on stdin cannot be a gate."}},
		// With a sudo password set, the stdin guard is off, but the dangerous
		// rules still ask for a confirm, like the approval gate.
		{"echo pw | sudo -S make install", true, true, GateCheck{Dangerous: true,
			Reason:      "⚠️ This gate matches a dangerous pattern: sudo with privilege flag (stdin/askpass/shell/list). The gate runs before each judge call, with no approval prompt. Add it?",
			Description: "sudo with privilege flag (stdin/askpass/shell/list)"}},
		{"rm -rf build && go test ./...", true, false, GateCheck{Dangerous: true,
			Reason:      "⚠️ This gate matches a dangerous pattern: recursive delete. The gate runs before each judge call, with no approval prompt. Add it?",
			Description: "recursive delete"}},
	}
	for _, c := range cases {
		if got := CheckGateCommand(c.cmd, c.inGateway, c.sudoSet); got != c.want {
			t.Errorf("CheckGateCommand(%q, %v, %v) = %+v, want %+v", c.cmd, c.inGateway, c.sudoSet, got, c.want)
		}
	}
}

func TestGateEdits(t *testing.T) {
	st := State{Goal: "ship it", Status: StatusActive, MaxTurns: 20, Epoch: 1}
	if n := st.AddGate("  go test ./...  "); n != 1 {
		t.Fatal(n)
	}
	if n := st.AddGate("make lint"); n != 2 {
		t.Fatal(n)
	}
	if n := st.AddGate("   "); n != 0 || len(st.Gates) != 2 {
		t.Fatal("an empty command adds no gate")
	}
	if g := st.Gates[0]; g.Command != "go test ./..." || g.TimeoutSeconds != 300 || g.MaxRetries != 3 || g.Attempts != 0 || g.LastExitCode != nil {
		t.Fatalf("%+v", g)
	}
	if got := RenderGates(st.Gates); got != "- 1. $ go test ./...\n- 2. $ make lint" {
		t.Fatalf("no result shows before the first run: %q", got)
	}
	pass, fail := 0, 2
	st.Gates[0].LastExitCode = &pass
	st.Gates[1].LastExitCode, st.Gates[1].Attempts = &fail, 1
	if got := RenderGates(st.Gates); got != "- 1. $ go test ./... ✓ passing\n- 2. $ make lint ✗ failing (exit 2, attempt 1/3)" {
		t.Fatalf("%q", got)
	}
	g, err := st.RemoveGate(1)
	if err != nil || g.Command != "go test ./..." || len(st.Gates) != 1 || st.Gates[0].Command != "make lint" {
		t.Fatal(g, err, st.Gates)
	}
	for _, n := range []int{0, 2, -1} {
		if _, err := st.RemoveGate(n); err == nil || err.Error() != "index out of range (1..1)" {
			t.Fatalf("RemoveGate(%d): %v", n, err)
		}
	}
	st.ClearGates()
	if len(st.Gates) != 0 {
		t.Fatal(st.Gates)
	}
	if got := RenderGates(st.Gates); got != "(no quality gates — use /goal gate add <command> to require one)" {
		t.Fatalf("%q", got)
	}
	if st.Epoch != 1 || st.Status != StatusActive {
		t.Fatal("a gate edit does not change the epoch or the status")
	}
}

func TestGateConstants(t *testing.T) {
	if GateTimeout.Seconds() != 300 || GateMaxRetries != 3 || GateOutputTail != 3000 {
		t.Fatal(GateTimeout, GateMaxRetries, GateOutputTail)
	}
}
