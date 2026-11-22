package media

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// tempFile makes a small file with the given name in a new folder.
func tempFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// defaultPolicy returns the policy with the default values of the gateway.media_* keys.
func defaultPolicy(data string, env map[string]string) *Policy {
	return NewPolicy(PolicyConfig{DataDir: data, Lookup: policyEnv(env), TrustRecent: true, TrustRecentS: 600})
}

// withEnv returns the policy of cfg with the variables of env.
func withEnv(cfg PolicyConfig, env map[string]string, log *slog.Logger) *Policy {
	cfg.Lookup, cfg.Log = policyEnv(env), log
	return NewPolicy(cfg)
}

func TestExtractMediaTagsAndFlags(t *testing.T) {
	png := tempFile(t, "chart.png")
	mp3 := tempFile(t, "reply.mp3")
	clean, files := openPolicy.ExtractMedia("Here.\nMEDIA:" + png + "\nMEDIA:" + mp3 + " [[audio_as_voice]]")
	if strings.Contains(clean, "MEDIA:") || strings.Contains(clean, "[[") || len(files) != 2 || files[0].Kind != "photo" || !files[1].AsVoice {
		t.Fatal(clean, files)
	}
	if clean != "Here." || files[0].Path != png || files[1].Kind != "audio" || files[0].AsVoice {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestExtractMediaQuotedAndSpaced(t *testing.T) {
	a := tempFile(t, "a.png")
	b := tempFile(t, "b.pdf")
	clean, files := openPolicy.ExtractMedia("See MEDIA:`" + a + "` and MEDIA: \"" + b + "\" now.")
	if len(files) != 2 || files[0].Path != a || files[1].Path != b || files[1].Kind != "document" {
		t.Fatalf("%q %+v", clean, files)
	}
	if strings.Contains(clean, "MEDIA") {
		t.Fatal(clean)
	}
}

func TestBarePathWithExtensionExtracted(t *testing.T) {
	pdf := tempFile(t, "report.pdf")
	clean, files := openPolicy.ExtractMedia("The report is ready: " + pdf + "\nRead it soon.")
	if len(files) != 1 || files[0].Path != pdf || files[0].Kind != "document" {
		t.Fatal(files)
	}
	if strings.Contains(clean, pdf) || !strings.Contains(clean, "Read it soon.") {
		t.Fatal(clean)
	}
}

func TestBarePathRules(t *testing.T) {
	pdf := tempFile(t, "report.pdf")
	cases := []string{
		"See https://example.com" + pdf,       // inside a URL
		"Run `cat " + pdf + "` first",         // inline code
		"```\n" + pdf + "\n```",               // fenced code
		"A missing /tmp/does/not/exist.pdf",   // no such file
		"Unknown " + filepath.Dir(pdf) + "/x", // no extension
	}
	for _, c := range cases {
		if clean, files := openPolicy.ExtractMedia(c); len(files) != 0 || clean != c {
			t.Errorf("%q → %q %+v", c, clean, files)
		}
	}
}

func TestMediaTagInCodeNotExtracted(t *testing.T) {
	// Hermes #35695: examples in code and quotes are not attachments.
	png := tempFile(t, "x.png")
	for _, text := range []string{
		"Use this form:\n```text\nMEDIA:" + png + "\n```",
		"Write `MEDIA:" + png + "` in the reply.",
		"> MEDIA:" + png,
	} {
		if clean, files := openPolicy.ExtractMedia(text); len(files) != 0 || clean != text {
			t.Error(clean, files)
		}
	}
}

func TestDuplicatePathsSentOnce(t *testing.T) {
	png := tempFile(t, "x.png")
	_, files := openPolicy.ExtractMedia("MEDIA:" + png + "\nAlso " + png)
	if len(files) != 1 {
		t.Fatal(files)
	}
}

func TestNonexistentPathIgnored(t *testing.T) {
	clean, files := openPolicy.ExtractMedia("Chart:\nMEDIA:/nope.png")
	if len(files) != 0 || !strings.Contains(clean, "[missing file: /nope.png]") || strings.Contains(clean, "MEDIA:") {
		t.Fatal(clean, files)
	}
}

func TestDirectoryNotSent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pics.png")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	clean, files := openPolicy.ExtractMedia("MEDIA:" + dir)
	if len(files) != 0 || !strings.Contains(clean, "[missing file: ") {
		t.Fatal(clean, files)
	}
}

func TestKindFromExtension(t *testing.T) {
	cases := map[string]string{
		"a.png": "photo", "a.JPG": "photo", "a.jpeg": "photo", "a.webp": "photo", "a.gif": "photo",
		"a.mp3": "audio", "a.ogg": "audio", "a.oga": "audio", "a.opus": "audio", "a.m4a": "audio", "a.wav": "audio",
		"a.mp4": "video", "a.mov": "video", "a.webm": "video",
		"a.pdf": "document", "a.zip": "document", "a.svg": "document",
	}
	for name, want := range cases {
		if got := SendKind(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
}

// openPolicy is a non-strict policy with no denied paths. It sends each regular file.
var openPolicy = &Policy{}

// policyEnv returns a lookup function for NewPolicy that reads only env.
func policyEnv(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

// writeAt makes the file p with its folders and sets its modification time to age ago.
func writeAt(t *testing.T, p string, age time.Duration) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestMediaPolicyRefusesProtectedPaths(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	pol := defaultPolicy(data, nil)
	for _, p := range []string{
		writeAt(t, filepath.Join(data, ".env"), time.Hour),
		writeAt(t, filepath.Join(data, "config.yaml"), time.Hour),
		writeAt(t, filepath.Join(data, "state.db"), time.Hour),
		writeAt(t, filepath.Join(data, "state.db-wal"), 0),
		writeAt(t, filepath.Join(data, "skills", "x", "SKILL.md"), 0),
		writeAt(t, filepath.Join(home, ".ssh", "id_ed25519"), time.Hour),
		writeAt(t, filepath.Join(home, ".aws", "credentials.txt"), 0),
		writeAt(t, filepath.Join(home, ".config", "gh", "hosts.yml"), 0),
	} {
		if _, err := pol.Check(p); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", p, err)
		}
		clean, files := pol.ExtractMedia("Here:\nMEDIA:`" + p + "`")
		if len(files) != 0 || !strings.Contains(clean, "[blocked file: "+p+"]") {
			t.Errorf("%s: %q %+v", p, clean, files)
		}
	}
	// A bare path of a protected file stays in the text and is not sent.
	cfg := filepath.Join(data, "config.yaml")
	if clean, files := pol.ExtractMedia("See " + cfg); len(files) != 0 || clean != "See "+cfg {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyProtectedBeatsAllowDir(t *testing.T) {
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	pol := defaultPolicy(data, map[string]string{EnvAllowDirs: data})
	if _, err := pol.Check(writeAt(t, filepath.Join(data, ".env"), 0)); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if _, err := pol.Check(writeAt(t, filepath.Join(data, "notes", "a.md"), time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestMediaPolicyCredentialDirsBeatAllowDir(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	pol := defaultPolicy(data, map[string]string{EnvAllowDirs: home})
	for _, p := range []string{
		writeAt(t, filepath.Join(home, ".aws", "credentials"), time.Hour),
		writeAt(t, filepath.Join(home, ".docker", "config.json"), time.Hour),
		writeAt(t, filepath.Join(home, ".gnupg", "secring.gpg"), time.Hour),
		writeAt(t, filepath.Join(home, ".SSH", "id_ed25519"), time.Hour),
	} {
		if _, err := pol.Check(p); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: %v, want ErrBlocked", p, err)
		}
	}
	// Another file in the allowed home folder is still sent.
	if _, err := pol.Check(writeAt(t, filepath.Join(home, "notes", "a.md"), time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestMediaPolicySymlinkToSecretRefused(t *testing.T) {
	data := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	secret := writeAt(t, filepath.Join(data, ".env"), 0)
	link := filepath.Join(t.TempDir(), "chart.png")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	pol := defaultPolicy(data, nil)
	if _, err := pol.Check(link); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if clean, files := pol.ExtractMedia("Chart: " + link); len(files) != 0 || !strings.Contains(clean, link) {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyDefaultModeSendsOtherFiles(t *testing.T) {
	// Hermes TestMediaDeliveryDefaultMode: a stale file outside the roots is sent.
	t.Setenv("HOME", t.TempDir())
	pol := defaultPolicy(t.TempDir(), nil)
	notes := writeAt(t, filepath.Join(t.TempDir(), "notes.md"), 2*time.Hour)
	got, err := pol.Check(notes)
	if err != nil || got != notes {
		t.Fatal(got, err)
	}
}

func TestMediaPolicyDeniedSystemPrefix(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeEtc := t.TempDir()
	pol := defaultPolicy(t.TempDir(), nil)
	pol.Denied = append(pol.Denied, fakeEtc)
	if _, err := pol.Check(writeAt(t, filepath.Join(fakeEtc, "shadow.txt"), 0)); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	for _, p := range []string{"/etc", "/proc", "/sys", "/dev", "/root", "/boot", "/var/log", "/var/lib", "/var/run"} {
		if !slices.Contains(pol.Denied, p) {
			t.Errorf("%s is not denied", p)
		}
	}
}

func TestMediaPolicyStrictMode(t *testing.T) {
	// Hermes TestMediaDeliveryPathValidation.
	t.Setenv("HOME", t.TempDir())
	data := t.TempDir()
	strict := func(extra map[string]string) *Policy {
		env := map[string]string{EnvStrict: "1"}
		maps.Copy(env, extra)
		return defaultPolicy(data, env)
	}
	noRecent := map[string]string{EnvTrustRecent: "0"}
	cached := writeAt(t, filepath.Join(data, "cache", "media", "voice.ogg"), 2*time.Hour)
	shot := writeAt(t, filepath.Join(data, "cache", "screenshots", "page.png"), 2*time.Hour)
	for _, p := range []string{cached, shot} {
		if got, err := strict(noRecent).Check(p); err != nil || got != p {
			t.Errorf("%s: %s %v", p, got, err)
		}
	}
	stale := writeAt(t, filepath.Join(t.TempDir(), "old.pdf"), 2*time.Hour)
	if _, err := strict(noRecent).Check(stale); !errors.Is(err, ErrBlocked) {
		t.Fatal("stale file outside the roots:", err)
	}
	fresh := writeAt(t, filepath.Join(t.TempDir(), "build", "report.pdf"), 0)
	if _, err := strict(nil).Check(fresh); err != nil {
		t.Fatal("fresh file with recency trust:", err)
	}
	if _, err := strict(noRecent).Check(fresh); !errors.Is(err, ErrBlocked) {
		t.Fatal("fresh file without recency trust:", err)
	}
	if _, err := strict(map[string]string{EnvTrustRecentSeconds: "60"}).Check(
		writeAt(t, filepath.Join(t.TempDir(), "r.pdf"), 2*time.Minute)); !errors.Is(err, ErrBlocked) {
		t.Fatal("file older than the window:", err)
	}
	extra := t.TempDir()
	inExtra := writeAt(t, filepath.Join(extra, "report.pdf"), 2*time.Hour)
	if _, err := strict(map[string]string{EnvTrustRecent: "0", EnvAllowDirs: "/nope," + extra}).Check(inExtra); err != nil {
		t.Fatal("file in TETHER_MEDIA_ALLOW_DIRS:", err)
	}
	// A fresh file in a denied folder is refused.
	sshKey := writeAt(t, filepath.Join(os.Getenv("HOME"), ".ssh", "id_rsa.txt"), 0)
	if _, err := strict(nil).Check(sshKey); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	// A link in a root to a file outside the roots is refused.
	link := filepath.Join(data, "cache", "media", "safe-looking.png")
	if err := os.Symlink(stale, link); err == nil {
		if _, err := strict(noRecent).Check(link); !errors.Is(err, ErrBlocked) {
			t.Fatal("symlink escape:", err)
		}
	}
}

func TestMediaPolicyStrictAliases(t *testing.T) {
	for raw, want := range map[string]bool{"1": true, "true": true, "TRUE": true, "yes": true, "on": true,
		"0": false, "false": false, "no": false, "off": false, "": false} {
		pol := defaultPolicy("", map[string]string{EnvStrict: raw})
		if pol.Strict != want {
			t.Errorf("%q: strict %v", raw, pol.Strict)
		}
	}
	if pol := defaultPolicy("", nil); pol.Strict || pol.TrustRecent != 10*time.Minute {
		t.Fatalf("%+v", pol)
	}
}

func TestNilMediaPolicySendsNothing(t *testing.T) {
	png := tempFile(t, "chart.png")
	var pol *Policy
	clean, files := pol.ExtractMedia("MEDIA:" + png)
	if len(files) != 0 || !strings.Contains(clean, "[blocked file: ") {
		t.Fatalf("%q %+v", clean, files)
	}
}

func TestMediaPolicyNullBytePath(t *testing.T) {
	good := tempFile(t, "good.png")
	_, files := openPolicy.ExtractMedia("here\nMEDIA:`~\x00evil.png`\nMEDIA:" + good)
	if len(files) != 1 || files[0].Path != good {
		t.Fatal(files)
	}
}

// policyLog returns a logger that writes to the returned buffer.
func policyLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestMediaPolicyKeysApplyWithoutVariables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	extra := t.TempDir()
	cfg := PolicyConfig{Strict: true, AllowDirs: []string{extra, "~/shared"}, TrustRecent: true, TrustRecentS: 60}
	pol := withEnv(cfg, nil, nil)
	if !pol.Strict || pol.TrustRecent != time.Minute {
		t.Fatalf("%+v", pol)
	}
	if !slices.Contains(pol.Roots, extra) || !slices.Contains(pol.Roots, filepath.Join(home, "shared")) {
		t.Fatalf("roots %v", pol.Roots)
	}
	// A folder of the key that is a symbolic link works through its real path.
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	strict := withEnv(PolicyConfig{Strict: true, AllowDirs: []string{link}}, nil, nil)
	if _, err := strict.Check(writeAt(t, filepath.Join(real, "report.pdf"), 2*time.Hour)); err != nil {
		t.Fatal("file in an allowed folder of the key:", err)
	}
}

func TestMediaPolicyVariableWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fromKey, fromVar := t.TempDir(), t.TempDir()
	cfg := PolicyConfig{AllowDirs: []string{fromKey}, TrustRecent: true, TrustRecentS: 600}
	env := map[string]string{EnvStrict: "TRUE", EnvAllowDirs: fromVar, EnvTrustRecent: "yes",
		EnvTrustRecentSeconds: "60"}
	pol := withEnv(cfg, env, nil)
	if !pol.Strict || pol.TrustRecent != time.Minute {
		t.Fatalf("%+v", pol)
	}
	if !slices.Contains(pol.Roots, fromVar) || slices.Contains(pol.Roots, fromKey) {
		t.Fatalf("roots %v", pol.Roots)
	}
	// A set variable also turns a key off.
	cfg = PolicyConfig{Strict: true, TrustRecent: true, TrustRecentS: 600}
	pol = withEnv(cfg, map[string]string{EnvStrict: "off"}, nil)
	if pol.Strict {
		t.Fatal("TETHER_MEDIA_DELIVERY_STRICT=off must win over the key")
	}
}

func TestMediaPolicyEmptyVariableDoesNotWin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fromKey := t.TempDir()
	cfg := PolicyConfig{Strict: true, AllowDirs: []string{fromKey}, TrustRecent: true, TrustRecentS: 60}
	env := map[string]string{EnvStrict: "", EnvAllowDirs: "", EnvTrustRecent: "", EnvTrustRecentSeconds: ""}
	log, buf := policyLog()
	pol := withEnv(cfg, env, log)
	if !pol.Strict || pol.TrustRecent != time.Minute || !slices.Contains(pol.Roots, fromKey) {
		t.Fatalf("%+v", pol)
	}
	if buf.Len() != 0 {
		t.Fatalf("an empty variable must not log: %s", buf.String())
	}
}

func TestMediaPolicyBadVariableKeepsKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	log, buf := policyLog()
	cfg := PolicyConfig{Strict: true, TrustRecent: true, TrustRecentS: 600}
	pol := withEnv(cfg, map[string]string{EnvStrict: "enabled"}, log)
	if !pol.Strict {
		t.Fatal("a value that is not valid must keep the strict key")
	}
	want := "media policy: TETHER_MEDIA_DELIVERY_STRICT has a value that is not valid; the config key gateway.media_delivery_strict applies"
	if !strings.Contains(buf.String(), want) || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("log %q", buf.String())
	}
	for raw, wantLog := range map[string]string{
		"1.5": "media policy: TETHER_MEDIA_TRUST_RECENT_SECONDS has a value that is not valid; the config key gateway.media_trust_recent_s applies",
		"-5":  "media policy: TETHER_MEDIA_TRUST_RECENT_SECONDS has a value that is not valid; the config key gateway.media_trust_recent_s applies",
	} {
		log, buf := policyLog()
		pol := withEnv(cfg, map[string]string{EnvTrustRecentSeconds: raw}, log)
		if pol.TrustRecent != 10*time.Minute || !strings.Contains(buf.String(), wantLog) {
			t.Fatalf("%q: window %s, log %q", raw, pol.TrustRecent, buf.String())
		}
	}
	log, buf = policyLog()
	pol = withEnv(cfg, map[string]string{EnvTrustRecent: "maybe"}, log)
	if pol.TrustRecent != 10*time.Minute ||
		!strings.Contains(buf.String(), "media policy: TETHER_MEDIA_TRUST_RECENT_FILES has a value that is not valid; the config key gateway.media_trust_recent_files applies") {
		t.Fatalf("window %s, log %q", pol.TrustRecent, buf.String())
	}
}

func TestMediaPolicyTrustOffGivesZeroWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pol := withEnv(PolicyConfig{Strict: true, TrustRecentS: 600}, nil, nil)
	if pol.TrustRecent != 0 {
		t.Fatalf("trust off in the key: window %s", pol.TrustRecent)
	}
	env := map[string]string{EnvTrustRecent: "off", EnvTrustRecentSeconds: "60"}
	cfg := PolicyConfig{Strict: true, TrustRecent: true, TrustRecentS: 600}
	if pol := withEnv(cfg, env, nil); pol.TrustRecent != 0 {
		t.Fatalf("trust off in the variable: window %s", pol.TrustRecent)
	}
	// A seconds value of 0 is valid and gives no recency trust.
	if pol := withEnv(cfg, map[string]string{EnvTrustRecentSeconds: "0"}, nil); pol.TrustRecent != 0 {
		t.Fatalf("seconds 0: window %s", pol.TrustRecent)
	}
}

// NewPolicy reads the data folder, the lookup, and the logger from the config.
func TestNewPolicyUsesConfigFields(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	log, buf := policyLog()
	pol := NewPolicy(PolicyConfig{DataDir: data, Lookup: policyEnv(nil), Log: log})
	want := []string{filepath.Join(data, "cache", "media"), filepath.Join(data, "cache", "screenshots")}
	if !slices.Equal(pol.Roots, want) || pol.Strict || pol.TrustRecent != 0 || pol.Log != log {
		t.Fatalf("%+v", pol)
	}
	if _, err := pol.Check(writeAt(t, filepath.Join(data, ".env"), 0)); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "media: a file is not sent: the media policy blocks it") {
		t.Fatalf("log %q", buf.String())
	}
}

// CheckHistory accepts only files in the media roots, of any age, in both modes.
func TestCheckHistoryNeedsARoot(t *testing.T) {
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, strict := range []bool{false, true} {
		pol := NewPolicy(PolicyConfig{DataDir: data, Lookup: policyEnv(nil), Strict: strict, TrustRecent: true, TrustRecentS: 600})
		old := writeAt(t, filepath.Join(data, "cache", "media", "chart.png"), 48*time.Hour)
		shot := writeAt(t, filepath.Join(data, "cache", "screenshots", "page.png"), 48*time.Hour)
		for _, p := range []string{old, shot} {
			if got, err := pol.CheckHistory(p); err != nil || got != p {
				t.Errorf("strict %v: %s: %q %v", strict, p, got, err)
			}
		}
		// A fresh file outside the roots goes out with a live answer, but not from history.
		fresh := writeAt(t, filepath.Join(t.TempDir(), "report.pdf"), 0)
		if _, err := pol.Check(fresh); err != nil {
			t.Errorf("strict %v: live check of a fresh file: %v", strict, err)
		}
		if _, err := pol.CheckHistory(fresh); !errors.Is(err, ErrBlocked) {
			t.Errorf("strict %v: history check of a file outside the roots: %v", strict, err)
		}
		if _, err := pol.CheckHistory(writeAt(t, filepath.Join(data, ".env"), 0)); !errors.Is(err, ErrBlocked) {
			t.Errorf("strict %v: protected path: %v", strict, err)
		}
		gone := filepath.Join(data, "cache", "media", "gone.png")
		if _, err := pol.CheckHistory(gone); err == nil || errors.Is(err, ErrBlocked) {
			t.Errorf("strict %v: a missing file is not blocked, it is missing: %v", strict, err)
		}
		// A link in a root to a file outside the roots is refused.
		link := filepath.Join(data, "cache", "media", fmt.Sprintf("link-%v.pdf", strict))
		if err := os.Symlink(fresh, link); err == nil {
			if _, err := pol.CheckHistory(link); !errors.Is(err, ErrBlocked) {
				t.Errorf("strict %v: symlink escape: %v", strict, err)
			}
		}
	}
	var nilPol *Policy
	if _, err := nilPol.CheckHistory(writeAt(t, filepath.Join(data, "cache", "media", "a.png"), 0)); !errors.Is(err, ErrBlocked) {
		t.Fatal("a nil policy refuses all files:", err)
	}
}
