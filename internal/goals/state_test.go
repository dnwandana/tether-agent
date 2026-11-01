package goals

import "testing"

func TestContractEmpty(t *testing.T) {
	if !(Contract{}).Empty() || !(Contract{Outcome: "  ", StopWhen: "\n"}).Empty() {
		t.Fatal("a contract with only blank fields must be empty")
	}
	if (Contract{Verification: "go test ./..."}).Empty() {
		t.Fatal("a contract with one field must not be empty")
	}
}

func TestSetStatusEpochAndReason(t *testing.T) {
	st := State{Goal: "g", Status: StatusActive, Epoch: 4}
	st.SetStatus(StatusPaused, "stopped by /stop")
	if st.Status != StatusPaused || st.PausedReason == nil || *st.PausedReason != "stopped by /stop" || st.Epoch != 5 {
		t.Fatalf("%+v", st)
	}
	st.SetStatus(StatusPaused, "")
	if st.PausedReason != nil || st.Epoch != 6 {
		t.Fatalf("an empty reason must give a nil PausedReason: %+v", st)
	}
	st.SetStatus(StatusPaused, "x")
	st.SetStatus(StatusActive, "ignored")
	if st.Status != StatusActive || st.PausedReason != nil || st.Epoch != 8 {
		t.Fatalf("a status other than paused must clear the reason: %+v", st)
	}
}

func TestClearWaitZeroesSixFields(t *testing.T) {
	pid, sess, reason := 4242, "4242", "ci"
	st := State{WaitingOnPID: &pid, WaitingOnSession: &sess, WaitingUntil: 1761955200.5,
		WaitingOnDelegations: 2, WaitingReason: &reason, WaitingSince: 1761955100.25}
	st.ClearWait()
	if st.WaitingOnPID != nil || st.WaitingOnSession != nil || st.WaitingUntil != 0 ||
		st.WaitingOnDelegations != 0 || st.WaitingReason != nil || st.WaitingSince != 0 {
		t.Fatalf("%+v", st)
	}
}

func TestResumeKeepsJudgeCounters(t *testing.T) {
	pid, reason := 99, "user-paused"
	st := State{Status: StatusPaused, PausedReason: &reason, TurnsUsed: 20, MaxTurns: 20, Epoch: 2,
		WaitingOnPID: &pid, ConsecutiveTurnFailures: 1,
		ConsecutiveParseFailures: 2, ConsecutiveTransportFailures: 4}
	st.Resume()
	if st.Status != StatusActive || st.PausedReason != nil || st.TurnsUsed != 0 || st.Epoch != 3 ||
		st.WaitingOnPID != nil || st.ConsecutiveTurnFailures != 0 {
		t.Fatalf("%+v", st)
	}
	if st.ConsecutiveParseFailures != 2 || st.ConsecutiveTransportFailures != 4 {
		t.Fatalf("Resume must keep the judge counters, like Hermes: %+v", st)
	}
}
