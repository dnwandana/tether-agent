package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceExt(t *testing.T) {
	for _, c := range []struct {
		head []byte
		want string
	}{
		{[]byte("OggS\x00\x02"), ".ogg"},
		{[]byte{0x1A, 0x45, 0xDF, 0xA3, 0x9F}, ".webm"},
		{[]byte("\x00\x00\x00\x1cftypM4A "), ".m4a"},
		{[]byte("\x00\x00\x00\x18ftypmp42"), ".m4a"},
		{[]byte("RIFF"), ""},
		{nil, ""},
	} {
		if got := voiceExt(c.head); got != c.want {
			t.Errorf("voiceExt(%q) = %q, want %q", c.head, got, c.want)
		}
	}
}

func TestConvertVoice(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	ff := filepath.Join(dir, "ffmpeg")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + args + "'\nfor last; do :; done\nprintf OggS > \"$last\"\n"
	if err := os.WriteFile(ff, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(dir, "1-ab.webm")
	if err := os.WriteFile(in, []byte{0x1A, 0x45, 0xDF, 0xA3}, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := convertVoice(context.Background(), ff, in)
	if err != nil || out != filepath.Join(dir, "1-ab.ogg") {
		t.Fatal(out, err)
	}
	if b, _ := os.ReadFile(out); string(b) != "OggS" {
		t.Fatalf("%q", b)
	}
	want := "-nostdin\n-y\n-loglevel\nerror\n-i\n" + in + "\n-c:a\nlibopus\n-b:a\n32k\n" + out + "\n"
	if b, _ := os.ReadFile(args); string(b) != want {
		t.Fatalf("%q", b)
	}
	if _, err := os.Stat(in); !os.IsNotExist(err) {
		t.Fatal("a good conversion removes the input file")
	}
	// A failed conversion keeps the input file and gives an error.
	bad := filepath.Join(dir, "ffmpeg-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	in2 := filepath.Join(dir, "2-cd.m4a")
	if err := os.WriteFile(in2, []byte("\x00\x00\x00\x18ftypmp42"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := convertVoice(context.Background(), bad, in2); err == nil || out != "" {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(in2); err != nil {
		t.Fatal("a failed conversion must keep the input file:", err)
	}
}
