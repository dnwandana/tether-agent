package skills

import "testing"

func TestCommandSlug(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"arxiv", "arxiv"},
		{"tether-memory", "tether-memory"},
		{"Git Helper", "git-helper"},
		{"git_helper", "git-helper"},
		{"pdf.tools", "pdftools"},
		{"小说拆条", ""},
		{"--a__b--", "a-b"},
		{"Git  Helper", "git-helper"},
		{"", ""},
	} {
		if got := CommandSlug(tc.name); got != tc.want {
			t.Errorf("CommandSlug(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSubstituteTemplateVars(t *testing.T) {
	const body = "Run ${HERMES_SKILL_DIR}/scripts/a.sh for ${HERMES_SESSION_ID}. Again: ${HERMES_SKILL_DIR}."
	for _, tc := range []struct{ name, body, dir, sessionID, want string }{
		{"both tokens", body, "/skills/arxiv", "s-1",
			"Run /skills/arxiv/scripts/a.sh for s-1. Again: /skills/arxiv."},
		{"empty session id keeps its token", body, "/skills/arxiv", "",
			"Run /skills/arxiv/scripts/a.sh for ${HERMES_SESSION_ID}. Again: /skills/arxiv."},
		{"empty dir keeps its token", body, "", "s-1",
			"Run ${HERMES_SKILL_DIR}/scripts/a.sh for s-1. Again: ${HERMES_SKILL_DIR}."},
		{"other tokens do not change",
			"${HOME} ${HERMES_OTHER} $HERMES_SKILL_DIR {HERMES_SESSION_ID} ${hermes_skill_dir}",
			"/skills/arxiv", "s-1",
			"${HOME} ${HERMES_OTHER} $HERMES_SKILL_DIR {HERMES_SESSION_ID} ${hermes_skill_dir}"},
		{"no tokens", "plain text", "/skills/arxiv", "s-1", "plain text"},
	} {
		if got := SubstituteTemplateVars(tc.body, tc.dir, tc.sessionID); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
