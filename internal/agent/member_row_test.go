package agent

import (
	"testing"

	"github.com/dnwandana/tether-agent/internal/provider"
)

// The user row of a turn records whether a member sent it, and the other rows do not.
func TestMemberOnUserRow(t *testing.T) {
	for _, member := range []bool{true, false} {
		fp := scripted(text("done"))
		a, sess := newAgent(t, fp)
		if _, err := a.RunTurn(ctx, Turn{Session: sess, UserText: "always restart nginx", Toolsets: all(),
			Member: member, Sink: &recSink{}}); err != nil {
			t.Fatal(err)
		}
		users := 0
		for _, m := range messages(t, a, sess.ID) {
			want := false
			if m.Msg.Role == provider.RoleUser {
				users++
				want = member
			}
			if m.Meta.Member != want {
				t.Fatal(member, m.Msg.Role, m.Meta.Member)
			}
		}
		if users != 1 {
			t.Fatal(member, users)
		}
	}
}
