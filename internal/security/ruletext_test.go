package security

import "testing"

func TestRuleText(t *testing.T) {
	t.Cleanup(func() { AddProtectedPathPatterns(nil) })
	AddProtectedPathPatterns([]string{"/srv/x/.env"})
	for _, c := range []struct {
		id, want string
		ok       bool
	}{
		{CouldNotReadPatternID, "could not read the command", true},
		{ScriptPatternID, "run a Python script on the host", true},
		{"sudo_stdin", "sudo password on stdin", true},
		{"acp:edit", "coding agent permission", true},
		{"recursive_delete", "recursive delete", true},
		{"git_force_push", "git force push (rewrites remote history)", true},
		{"sqlite_client_state_db", "SQLite client on state.db", true},
		{"recursive_delete_home_directory", "recursive delete of home directory", true},
		{"human_only_password_change", "password change", true},
		{"gateway_self_kill", "kill own gateway process", true},
		{"protected_path_write_srv_x_env", "write to protected path /srv/x/.env", true},
		{"old_rule_x", "", false},
		{"", "", false},
	} {
		got, ok := RuleText(c.id)
		if got != c.want || ok != c.ok {
			t.Errorf("RuleText(%q) = %q, %v; want %q, %v", c.id, got, ok, c.want, c.ok)
		}
	}
}
