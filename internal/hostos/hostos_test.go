package hostos

import (
	"io/fs"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name, goos, osRelease string
		want                  System
	}{
		{"macos", "darwin", "", MacOS},
		{"ubuntu", "linux", "ID=ubuntu\nID_LIKE=debian\n", Ubuntu},
		{"debian", "linux", "ID=debian\n", Debian},
		{"mint", "linux", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\n", Ubuntu},
		{"raspbian", "linux", "ID=raspbian\nID_LIKE=debian\n", Debian},
		{"fedora", "linux", "ID=fedora\n", Other},
		{"quoted", "linux", "ID=\"debian\"\n", Debian},
		{"single quotes", "linux", "ID='ubuntu'\n", Ubuntu},
		{"comments and crlf", "linux", "# comment\r\n\r\nNAME=\"Ubuntu\"\r\nID=ubuntu\r\n", Ubuntu},
		{"missing file", "linux", "<missing>", Other},
		{"windows", "windows", "", Other},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readFile := func(name string) ([]byte, error) {
				if name != "/etc/os-release" || tc.osRelease == "<missing>" {
					return nil, fs.ErrNotExist
				}
				return []byte(tc.osRelease), nil
			}
			if got := Detect(tc.goos, readFile); got != tc.want {
				t.Fatalf("Detect(%q) = %q, want %q", tc.goos, got, tc.want)
			}
		})
	}
}

func TestSetRestores(t *testing.T) {
	before := Current()
	restore := Set(MacOS)
	if got := Current(); got != MacOS {
		t.Fatalf("Current() after Set = %q, want %q", got, MacOS)
	}
	restore()
	if got := Current(); got != before {
		t.Fatalf("Current() after restore = %q, want %q", got, before)
	}
}
