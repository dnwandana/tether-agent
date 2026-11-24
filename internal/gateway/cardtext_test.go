package gateway

import (
	"testing"
	"time"
)

func TestCardResultHeader(t *testing.T) {
	for _, c := range []struct {
		r    CardResult
		want string
	}{
		{CardResult{Kind: "approval", Scope: "once"}, "✅ Approval allowed · once"},
		{CardResult{Kind: "approval", Scope: "session"}, "✅ Approval allowed · this session"},
		{CardResult{Kind: "approval", Scope: "always"}, "✅ Approval allowed · always"},
		{CardResult{Kind: "send", Scope: "once"}, "✅ Approval allowed · once"},
		{CardResult{Kind: "send", Scope: "redacted"}, "✅ Sent redacted"},
		{CardResult{Kind: "approval", Scope: "deny"}, "❌ Approval denied"},
		{CardResult{Kind: "approval", Scope: "unknown"}, "❌ Approval denied"},
		{CardResult{Kind: "approval", Scope: "timeout", Wait: 10 * time.Minute}, "⌛ Approval denied · no answer in 10 min"},
		{CardResult{Kind: "approval", Scope: "timeout"}, "⌛ Approval denied · no answer"},
		{CardResult{Kind: "approval", Scope: "timeout", Wait: 45 * time.Second}, "⌛ Approval denied · no answer in 45 s"},
		{CardResult{Kind: "pin", Scope: "once"}, "✅ Program allowed"},
		{CardResult{Kind: "pin", Scope: "session"}, "✅ Program allowed"},
		{CardResult{Kind: "pin", Scope: "deny"}, "❌ Program denied"},
		{CardResult{Kind: "pin", Scope: "timeout", Wait: 10 * time.Minute}, "⌛ Program denied · no answer in 10 min"},
		{CardResult{Kind: "pin", Scope: "timeout"}, "⌛ Program denied · no answer"},
		{CardResult{Kind: "approval", Scope: "deny", Stopped: true}, "⏹ No answer · the turn stopped"},
		{CardResult{Kind: "pin", Scope: "timeout", Wait: 10 * time.Minute, Stopped: true}, "⏹ No answer · the turn stopped"},
		{CardResult{Kind: "send", Scope: "once", Stopped: true}, "⏹ No answer · the turn stopped"},
	} {
		if got := CardResultHeader(c.r); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.r, got, c.want)
		}
	}
}
