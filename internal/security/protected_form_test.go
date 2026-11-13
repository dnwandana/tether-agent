package security

import (
	"slices"
	"testing"
)

func TestSecondForm(t *testing.T) {
	paths := []string{"/home/d/.ssh/", "~/.ssh/", "/home/d/.bashrc", "~/.bashrc",
		"/home/d/.tether/state.db", "~/.tether/state.db", "~/.tether/skills/"}
	for in, want := range map[string]string{
		`sqlite3 ~/.tether/"state".db x`:      `sqlite3 ~/.tether/state.db x`,
		`sqlite3 ~/.tether/state\.db .tables`: `sqlite3 ~/.tether/state.db .tables`,
		`sqlite3 ~/.tether/state* .tables`:    `sqlite3 ~/.tether/state.db .tables`,
		`cp /tmp/x ~/.tether/stat?.db`:        `cp /tmp/x ~/.tether/state.db`,
		`rm -rf ~/.ss*`:                       `rm -rf ~/.ssh`,
		`cat ~/.ss?/authorized_keys`:          `cat ~/.ssh/authorized_keys`,
		`mv ~/.teth*/skills/a/x /tmp`:         `mv ~/.tether/skills/a/x /tmp`,
		`rm ~/.*`:                             `rm ~/.ssh ~/.bashrc`,
		`rm /home/*/.bashrc`:                  `rm /home/d/.bashrc`,
		`(cat ~/.ss*)`:                        `(cat ~/.ssh)`,
		`cd ~/.tether && sqlite3 state*`:      `cd ~/.tether && sqlite3 state.db`,
		`rm ~/*`:                              `rm ~/*`,
		`ls ~/projects/*`:                     `ls ~/projects/*`,
		`ls *.db`:                             `ls state.db`,
		`sqlite3 app.db "select * from t"`:    `sqlite3 app.db select * from t`,
		"`cp x ~/.bashr?`":                    "`cp x ~/.bashrc`",
		`cat ~/.ss[!x]/id`:                    `cat ~/.ssh/id`,
		`cat ~/.ss[^x]/id`:                    `cat ~/.ssh/id`,
		`cat ~/.bash[[:lower:]]c`:             `cat ~/.bashrc`,
		`cat ~/.bash[[:digit:]]c`:             `cat ~/.bash[[:digit:]]c`,
		`cat ~/.bash[-r]c`:                    `cat ~/.bashrc`,
		`echo "state"`:                        `echo state`,
		`[ -f x ]`:                            `[ -f x ]`,
		`[`:                                   `[`,
		`a[`:                                  `a[`,
		`\[`:                                  `[`,
	} {
		if got := secondForm(in, paths); got != want {
			t.Errorf("secondForm(%q) = %q, want %q", in, got, want)
		}
	}
	if got := secondForm(`ls "a b"`, nil); got != `ls a b` {
		t.Fatal("no paths still removes the quotes:", got)
	}
}

func TestSecondFormAllMatches(t *testing.T) {
	paths := []string{"~/.tether/config.yaml", "~/.tether/.env", "~/.tether/state.db", "~/.tether/skills/"}
	got := secondForm(`rm ~/.tether/*`, paths)
	if want := `rm ~/.tether/config.yaml ~/.tether/state.db ~/.tether/skills`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestHasLiteral(t *testing.T) {
	for in, want := range map[string]bool{
		"*": false, "?*": false, "[a-z]*": false, "[[:lower:]]": false, "[]x]": false,
		"*.db": true, "s*": true, "[": true, "a[": true,
	} {
		if got := hasLiteral(in); got != want {
			t.Errorf("hasLiteral(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBareStarIsNotStateDB(t *testing.T) {
	t.Cleanup(func() { AddProtectedPathPatterns(nil) })
	AddProtectedPathPatterns([]string{"/home/d/.tether/state.db", "/home/d/.tether/config.yaml"})
	for _, p := range MatchAllDangerous(`sqlite3 app.db "SELECT * FROM users"`) {
		if IsStateDBPatternID(p.ID) {
			t.Errorf("a bare * matched %s", p.ID)
		}
	}
	if !slices.ContainsFunc(MatchAllDangerous(`cd /home/d/.tether && sqlite3 *.db .tables`), func(p Pattern) bool {
		return IsStateDBPatternID(p.ID)
	}) {
		t.Error("*.db must still match the state.db rules")
	}
}

func TestGlobWordCoversEachProtectedPath(t *testing.T) {
	t.Cleanup(func() { AddProtectedPathPatterns(nil) })
	AddProtectedPathPatterns([]string{"/home/d/.tether/config.yaml", "/home/d/.tether/state.db"})
	ids := map[string]bool{}
	for _, p := range MatchAllDangerous(`rm /home/d/.tether/*`) {
		ids[p.ID] = true
	}
	for _, id := range []string{"protected_path_write_home_d_tether_config_yaml", "protected_path_write_home_d_tether_state_db"} {
		if !ids[id] {
			t.Errorf("rm /home/d/.tether/* did not match %s: %v", id, ids)
		}
	}
}
