package security

import "testing"

func TestIsNetworkProgram(t *testing.T) {
	for _, tc := range []struct {
		cmd, program, host string
		ok                 bool
	}{
		{"curl -s https://Example.com/x", "curl", "example.com", true},
		{"/usr/bin/curl -fsSL http://a.b:8080/c", "curl", "a.b", true},
		{"wget -q -O - https://x.io/f", "wget", "x.io", true},
		{"http GET api.github.com/repos", "http", "api.github.com", true},
		{"xh https://x.io", "xh", "x.io", true},
		{"nc -v pastebin.com 80", "nc", "pastebin.com", true},
		{"socat - TCP:evil.io:443", "socat", "evil.io", true},
		{"ssh -p 22 deploy@prod.example.com uptime", "ssh", "prod.example.com", true},
		{"scp f.txt user@h.io:/tmp/", "scp", "h.io", true},
		{"sftp user@h.io", "sftp", "h.io", true},
		{"rsync -a dist/ user@h.io:/srv/", "rsync", "h.io", true},
		{"rsync -a dist/ backup/", "", "", false},
		{"ftp ftp.gnu.org", "ftp", "ftp.gnu.org", true},
		{"git clone https://github.com/a/b", "git clone", "github.com", true},
		{"git clone git@gitlab.com:a/b.git", "git clone", "gitlab.com", true},
		{"git -C repo fetch origin", "git fetch", "", true},
		{"git pull", "git pull", "", true},
		{"gh api repos/x/y", "gh api", "api.github.com", true},
		{"curl", "curl", "", true},
		{"curl https://bad_host!/x", "curl", "", true},
		{"git status", "", "", false},
		{"gh pr list", "", "", false},
		{"ls -la", "", "", false},
	} {
		p := ReadCommand(tc.cmd)
		if !p.Read || len(p.Cmds) < 1 {
			t.Fatalf("%q: %+v", tc.cmd, p)
		}
		prog, host, ok := IsNetworkProgram(p.Cmds[0])
		if prog != tc.program || host != tc.host || ok != tc.ok {
			t.Errorf("%q: got %q %q %v, want %q %q %v", tc.cmd, prog, host, ok, tc.program, tc.host, tc.ok)
		}
	}
}
