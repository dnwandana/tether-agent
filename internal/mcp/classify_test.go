package mcp

import "testing"

func TestClassValues(t *testing.T) {
	if ClassRead != "read" || ClassWrite != "write" || ClassSend != "send" || ClassHumanOnly != "human-only" {
		t.Fatal(ClassRead, ClassWrite, ClassSend, ClassHumanOnly)
	}
}

func TestClassify(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name, server string
		readOnly     bool
		destructive  *bool
		want         Class
	}{
		// The names of real servers.
		{"slack_list_channels", "slack", false, nil, ClassRead},
		{"slack_post_message", "slack", false, nil, ClassSend},
		{"chat_post_message", "slack", false, nil, ClassSend},
		{"notion-search", "notion", false, nil, ClassRead},
		{"API-post-search", "notion", false, nil, ClassRead},
		{"API-patch-page", "notion", false, nil, ClassWrite},
		{"get_charge", "stripe", false, nil, ClassRead},
		{"retrieve_refund", "stripe", false, nil, ClassRead},
		{"get_checkout_session", "stripe", false, nil, ClassRead},
		{"get_account", "stripe", false, nil, ClassRead},
		{"create_refund", "stripe", false, nil, ClassHumanOnly},
		{"create_invoice", "stripe", false, nil, ClassSend},
		{"transfer_issue", "github", false, nil, ClassWrite},
		{"delete_repository", "github", false, nil, ClassWrite},
		{"create_comment", "github", false, nil, ClassSend},
		{"add_collaborator", "github", false, nil, ClassHumanOnly},
		{"listIssues", "github", false, nil, ClassRead},
		{"send_email", "gmail", false, nil, ClassSend},
		{"create_draft", "gmail", false, nil, ClassWrite},
		{"delete_all_messages", "gmail", false, nil, ClassHumanOnly},

		// The human-only word sets.
		{"pay_invoice", "stripe", false, nil, ClassHumanOnly},
		{"checkout_cart", "shop", false, nil, ClassHumanOnly},
		{"send_money", "bank", false, nil, ClassHumanOnly},
		{"createRefund", "stripe", false, nil, ClassHumanOnly},
		{"invite_member", "slack", false, nil, ClassHumanOnly},
		{"rotate_key", "vault", false, nil, ClassHumanOnly},
		{"reveal_password", "vault", false, nil, ClassHumanOnly},
		{"reset_password", "auth", false, nil, ClassHumanOnly},
		{"purge_cache", "cdn", false, nil, ClassHumanOnly},
		{"empty_trash", "drive", false, nil, ClassHumanOnly},

		// The prefix drop and the HTTP method.
		{"post_search", "notion", false, nil, ClassSend},
		{"mcp_get_file", "fs", false, nil, ClassRead},
		{"get_stats", "sendgrid", false, nil, ClassRead},

		// The hints.
		{"get_thing", "x", false, &yes, ClassWrite},
		{"get_thing", "x", false, &no, ClassRead},
		{"conversations_history", "slack", true, nil, ClassRead},
		{"conversations_history", "slack", true, &yes, ClassWrite},
		{"conversations_history", "slack", false, nil, ClassWrite},
		{"update_status", "slack", true, nil, ClassWrite},
	}
	for _, c := range cases {
		if got := Classify(c.name, c.server, c.readOnly, c.destructive); got != c.want {
			d := "nil"
			if c.destructive != nil {
				d = map[bool]string{true: "true", false: "false"}[*c.destructive]
			}
			t.Errorf("Classify(%q, %q, %v, %s) = %q, want %q", c.name, c.server, c.readOnly, d, got, c.want)
		}
	}
}
