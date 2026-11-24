package gateway

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/security"
)

// secretsDeps returns deps with three bound names: GH_TOKEN is set, NPM_TOKEN has no value,
// and PYPI_TOKEN is set, marked, and pinned.
func secretsDeps(started, unpinned, unpinnedPaths *[]string) SecretsDeps {
	mark := time.Date(2026, 11, 18, 12, 31, 0, 0, time.Local)
	return SecretsDeps{
		Bound: func() map[string][]string {
			return map[string][]string{
				"PYPI_TOKEN": {"twine upload", "uv publish"},
				"GH_TOKEN":   {"gh release", "gh pr create"},
				"NPM_TOKEN":  {"npm publish"},
			}
		},
		HasValue: func(name string) bool { return name != "NPM_TOKEN" },
		Marks: func(context.Context) (map[string]time.Time, error) {
			return map[string]time.Time{"PYPI_TOKEN": mark}, nil
		},
		Pins: func(_ context.Context, name string) ([]security.PinTarget, error) {
			if name == "PYPI_TOKEN" {
				return []security.PinTarget{{Path: "/home/user/.local/bin/twine", SHA256: "3f2a"}}, nil
			}
			return nil, nil
		},
		Unpin: func(_ context.Context, name string) (int64, error) {
			*unpinned = append(*unpinned, name)
			if name == "PYPI_TOKEN" {
				return 2, nil
			}
			return 0, nil
		},
		UnpinPath: func(_ context.Context, name, path string) (int64, error) {
			*unpinnedPaths = append(*unpinnedPaths, name+"|"+path)
			if name == "PYPI_TOKEN" && path == "/home/user/my tools/twine" {
				return 1, nil
			}
			return 0, nil
		},
		Start: func(_ context.Context, _ *CommandCtx, name string) error {
			*started = append(*started, name)
			return nil
		},
	}
}

func TestSecretsList(t *testing.T) {
	var started, unpinned, paths []string
	registerTestCommand(t, SecretsCommand(secretsDeps(&started, &unpinned, &paths)))
	c, sent := interactiveCtx(t)
	run(t, c, "/secrets")
	want := "🔑 Bound secrets\n" +
		"• GH\\_TOKEN: set · gh release, gh pr create\n" +
		"• NPM\\_TOKEN: not set · npm publish\n" +
		"• PYPI\\_TOKEN: ⚠️ check (auth failure Nov 18 12:31) · twine upload, uv publish\n" +
		"  Programs: /home/user/.local/bin/twine\n" +
		"Send /secrets set NAME to save a new value."
	if r := lastSent(t, sent); r.Text != want || r.Buttons != nil {
		t.Fatalf("got:\n%s", r.Text)
	}
	cmd := Commands()["secrets"]
	if cmd.MemberOK || !cmd.WhileBusy || cmd.Help != "See and save the bound secrets: /secrets [set NAME | unpin NAME [PATH]]" {
		t.Fatalf("%+v", cmd)
	}
}

func TestSecretsSetAndUnpin(t *testing.T) {
	var started, unpinned, paths []string
	registerTestCommand(t, SecretsCommand(secretsDeps(&started, &unpinned, &paths)))
	c, sent := interactiveCtx(t)
	run(t, c, "/secrets set PYPI_TOKEN")
	if len(started) != 1 || started[0] != "PYPI_TOKEN" || len(*sent) != 0 {
		t.Fatal(started, *sent)
	}
	run(t, c, "/secrets set AWS_KEY")
	if r := lastSent(t, sent); r.Text != "\"AWS\\_KEY\" is not a bound secret. The owner binds secrets in exec\\_secrets in config.yaml." {
		t.Fatal(r.Text)
	}
	run(t, c, "/secrets unpin PYPI_TOKEN")
	if r := lastSent(t, sent); r.Text != "Removed 2 allowed programs of PYPI\\_TOKEN. The next use asks again." {
		t.Fatal(r.Text)
	}
	run(t, c, "/secrets unpin GH_TOKEN")
	if r := lastSent(t, sent); r.Text != "GH\\_TOKEN has no allowed programs." {
		t.Fatal(r.Text)
	}
	run(t, c, "/secrets drop")
	if r := lastSent(t, sent); r.Text != "See and save the bound secrets: /secrets [set NAME | unpin NAME [PATH]]" {
		t.Fatal(r.Text)
	}
}

func TestSecretsRefusedInGroup(t *testing.T) {
	var started, unpinned, paths []string
	registerTestCommand(t, SecretsCommand(secretsDeps(&started, &unpinned, &paths)))
	c, sent := interactiveCtx(t)
	c.Key = "telegram:group:-100"
	for _, text := range []string{"/secrets", "/secrets set PYPI_TOKEN", "/secrets unpin PYPI_TOKEN"} {
		run(t, c, text)
		if r := lastSent(t, sent); r.Text != "Send /secrets in a private chat with me." {
			t.Fatal(text, r.Text)
		}
	}
	if len(started) != 0 || len(unpinned) != 0 {
		t.Fatal(started, unpinned)
	}
}

func TestSecretsNoBoundNames(t *testing.T) {
	d := SecretsDeps{Bound: func() map[string][]string { return nil }}
	registerTestCommand(t, SecretsCommand(d))
	c, sent := interactiveCtx(t)
	run(t, c, "/secrets")
	if r := lastSent(t, sent); r.Text != "🔑 No bound secrets. Bind a secret in exec\\_secrets in config.yaml." {
		t.Fatal(r.Text)
	}
}

// /secrets unpin NAME PATH removes one program. PATH is the rest of the line, with its spaces.
func TestSecretsUnpinPath(t *testing.T) {
	var started, unpinned, paths []string
	registerTestCommand(t, SecretsCommand(secretsDeps(&started, &unpinned, &paths)))
	c, sent := interactiveCtx(t)
	run(t, c, "/secrets unpin PYPI_TOKEN   /home/user/my tools/twine  ")
	if r := lastSent(t, sent); r.Text != "Removed the allowed program `/home/user/my tools/twine` of PYPI\\_TOKEN. The next use asks again." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/secrets unpin PYPI_TOKEN /usr/bin/uv")
	if r := lastSent(t, sent); r.Text != "PYPI\\_TOKEN has no allowed program `/usr/bin/uv`." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/secrets unpin AWS_KEY /usr/bin/aws")
	if r := lastSent(t, sent); r.Text != "\"AWS\\_KEY\" is not a bound secret. The owner binds secrets in exec\\_secrets in config.yaml." {
		t.Fatalf("got:\n%s", r.Text)
	}
	run(t, c, "/secrets unpin PYPI_TOKEN /usr/local/bin/my_tool")
	if r := lastSent(t, sent); r.Text != "PYPI\\_TOKEN has no allowed program `/usr/local/bin/my_tool`." {
		t.Fatalf("got:\n%s", r.Text)
	}
	want := []string{"PYPI_TOKEN|/home/user/my tools/twine", "PYPI_TOKEN|/usr/bin/uv", "PYPI_TOKEN|/usr/local/bin/my_tool"}
	if !reflect.DeepEqual(paths, want) || len(unpinned) != 0 {
		t.Fatal(paths, unpinned)
	}
}
