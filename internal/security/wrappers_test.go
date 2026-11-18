package security

import (
	"slices"
	"strings"
	"testing"
)

// nestShells returns "ls" inside n levels of sh -c.
func nestShells(n int) string {
	cmd := "ls"
	for range n {
		cmd = "sh -c '" + strings.ReplaceAll(cmd, "'", `'\''`) + "'"
	}
	return cmd
}

func TestLookupWrapper(t *testing.T) {
	for name, want := range map[string]WrapperKind{
		"nice": WrapTransparent, "env": WrapTransparent, "bash": WrapTransparent, "ash": WrapTransparent,
		"eval": WrapTransparent, "xargs": WrapTransparent, "script": WrapTransparent, "time": WrapTransparent,
		"sudo": WrapPrivilege, "doas": WrapPrivilege, "su": WrapPrivilege, "runuser": WrapPrivilege, "pkexec": WrapPrivilege,
		"ssh": WrapHost, "docker": WrapHost, "podman": WrapHost, "kubectl": WrapHost, "chroot": WrapHost, "nsenter": WrapHost,
	} {
		if got, ok := LookupWrapper(name); !ok || got != want {
			t.Errorf("%s: %v %v, want %v", name, got, ok, want)
		}
	}
	for _, name := range []string{"ls", "rm", "", "sudox"} {
		if _, ok := LookupWrapper(name); ok {
			t.Errorf("%q is a wrapper", name)
		}
	}
}

func TestWrapperLookThrough(t *testing.T) {
	for _, tc := range []struct {
		in    string
		argv  []string // the Argv of the last SimpleCmd
		via   []string // the Via of the last SimpleCmd
		depth int      // the Depth of the last SimpleCmd
		key   string   // the ViaKey of the command
	}{
		{"timeout 60 sh -c 'rm -rf /srv/app'", []string{"rm", "-rf", "/srv/app"}, nil, 1, ""},
		{"sudo rm -rf /srv/app", []string{"rm", "-rf", "/srv/app"}, []string{"sudo"}, 0, "sudo"},
		{"/usr/bin/sudo -u root rm x", []string{"rm", "x"}, []string{"sudo"}, 0, "sudo"},
		{"sudo -E reboot", []string{"reboot"}, []string{"sudo"}, 0, "sudo"},
		{"ssh prod 'systemctl restart nginx'", []string{"systemctl", "restart", "nginx"}, []string{"ssh prod"}, 1, "ssh prod"},
		{"ssh -p 2222 -i ~/.ssh/k deploy@prod uptime", []string{"uptime"}, []string{"ssh deploy@prod"}, 1, "ssh deploy@prod"},
		{"docker exec web sh -c 'rm -rf /data'", []string{"rm", "-rf", "/data"}, []string{"docker exec web"}, 1, "docker exec web"},
		{"docker exec -it -u root web ls", []string{"ls"}, []string{"docker exec web"}, 0, "docker exec web"},
		{"podman exec -e A=1 db psql", []string{"psql"}, []string{"podman exec db"}, 0, "podman exec db"},
		{"kubectl exec -n prod api-0 -- ls /", []string{"ls", "/"}, []string{"kubectl exec api-0"}, 0, "kubectl exec api-0"},
		{"chroot /srv/root ls", []string{"ls"}, []string{"chroot /srv/root"}, 0, "chroot /srv/root"},
		{"nsenter -t 1 -m ls", []string{"ls"}, []string{"nsenter"}, 0, "nsenter"},
		{"sudo ssh prod ls", []string{"ls"}, []string{"sudo", "ssh prod"}, 1, "ssh prod,sudo"},
		{"su -c 'rm x' root", []string{"rm", "x"}, []string{"su"}, 1, "su"},
		{"doas rm x", []string{"rm", "x"}, []string{"doas"}, 0, "doas"},
		{"pkexec rm x", []string{"rm", "x"}, []string{"pkexec"}, 0, "pkexec"},
		{"runuser -u app -- rm x", []string{"rm", "x"}, []string{"runuser"}, 0, "runuser"},
		{"env FOO=1 BAR=2 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"env -S 'rm -rf x'", []string{"rm", "-rf", "x"}, nil, 0, ""},
		{"xargs -0 rm -f", []string{"rm", "-f"}, nil, 0, ""},
		{"nohup nice -n 5 make", []string{"make"}, nil, 0, ""},
		{"eval 'rm -rf x'", []string{"rm", "-rf", "x"}, nil, 1, ""},
		{"script -q -c 'rm x' /dev/null", []string{"rm", "x"}, nil, 1, ""},
		{"bash -lc 'make test'", []string{"make", "test"}, nil, 1, ""},
		{"flock /tmp/l rm x", []string{"rm", "x"}, nil, 0, ""},
		{"stdbuf -oL tail -f log", []string{"tail", "-f", "log"}, nil, 0, ""},
		{"busybox rm x", []string{"rm", "x"}, nil, 0, ""},
		{"strace -f -o out rm x", []string{"rm", "x"}, nil, 0, ""},
		{"watch -n 1 ls", []string{"ls"}, nil, 1, ""},
		{"command rm x", []string{"rm", "x"}, nil, 0, ""},
		{"exec rm x", []string{"rm", "x"}, nil, 0, ""},
		{`bash -c "cd $DIR && make"`, []string{"make"}, nil, 1, ""},
		{"sudo apt update && rm -rf build", []string{"rm", "-rf", "build"}, nil, 0, "sudo"},
		{"flock /tmp/l -c 'rm x'", []string{"rm", "x"}, nil, 1, ""},
		{"flock -n -w 5 /tmp/l rm x", []string{"rm", "x"}, nil, 0, ""},
		{"script -q /dev/null -c 'rm x'", []string{"rm", "x"}, nil, 1, ""},
		{"script -q /dev/null rm x", []string{"rm", "x"}, nil, 0, ""},
		{"su root -- -c 'rm x'", []string{"rm", "x"}, []string{"su"}, 1, "su"},
		{"su - root -c 'rm x'", []string{"rm", "x"}, []string{"su"}, 1, "su"},
		{"runuser -u app -- rm x", []string{"rm", "x"}, []string{"runuser"}, 0, "runuser"},
		{"ssh prod -t 'rm x'", []string{"rm", "x"}, []string{"ssh prod"}, 1, "ssh prod"},
		{"ssh prod -- rm x", []string{"rm", "x"}, []string{"ssh prod"}, 1, "ssh prod"},
		{"ssh -- prod -t", []string{"-t"}, []string{"ssh prod"}, 1, "ssh prod"},
		{"ssh -o ProxyCommand='ssh -W %h:%p jump' prod ls", []string{"ls"}, []string{"ssh prod"}, 1, "ssh prod"},
		{"sudo FOO=1 BAR=2 rm x", []string{"rm", "x"}, []string{"sudo"}, 0, "sudo"},
		{"kubectl exec api-0 -c app -- rm x", []string{"rm", "x"}, []string{"kubectl exec api-0"}, 0, "kubectl exec api-0"},
		{"kubectl -n prod exec -it api-0 -- rm x", []string{"rm", "x"}, []string{"kubectl exec api-0"}, 0, "kubectl exec api-0"},
		{"docker container exec web rm x", []string{"rm", "x"}, []string{"docker exec web"}, 0, "docker exec web"},
		{"fish -c 'rm x'", []string{"rm", "x"}, nil, 1, ""},
		{"zsh -fc 'rm x'", []string{"rm", "x"}, nil, 1, ""},
		{"bash --norc -o pipefail -c 'rm x'", []string{"rm", "x"}, nil, 1, ""},
		{"xargs -I{} rm {}", []string{"rm", "{}"}, nil, 0, ""},
		{"xargs --replace={} rm {}", []string{"rm", "{}"}, nil, 0, ""},
		{"strace -e trace=open -s 100 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"unshare -r -R /srv rm x", []string{"rm", "x"}, nil, 0, ""},
		{"taskset -c 0,1 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"taskset 0x3 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"nice -5 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"timeout -k 5 -s KILL 60 rm x", []string{"rm", "x"}, nil, 0, ""},
		{"watch -x rm x", []string{"rm", "x"}, nil, 0, ""},
		{"nsenter -t 1 -m -u rm x", []string{"rm", "x"}, []string{"nsenter"}, 0, "nsenter"},
	} {
		p := ReadCommand(tc.in)
		if !p.Read || len(p.Cmds) == 0 {
			t.Errorf("%q: read=%v why=%q", tc.in, p.Read, p.Why)
			continue
		}
		last := p.Cmds[len(p.Cmds)-1]
		if !slices.Equal(last.Argv, tc.argv) || !slices.Equal(last.Via, tc.via) || last.Depth != tc.depth || p.ViaKey() != tc.key {
			t.Errorf("%q: last=%+v key=%q", tc.in, last, p.ViaKey())
		}
	}
}

// Each wrapper level adds one SimpleCmd, right after the command that holds it.
func TestWrapperAddsOneCmdForEachLevel(t *testing.T) {
	p := ReadCommand("timeout 60 sh -c 'rm -rf /srv/app'")
	want := [][]string{
		{"timeout", "60", "sh", "-c", "rm -rf /srv/app"},
		{"sh", "-c", "rm -rf /srv/app"},
		{"rm", "-rf", "/srv/app"},
	}
	if !p.Read || !sameArgvs(argvs(p), want) || p.Cmds[0].Depth != 0 || p.Cmds[1].Depth != 0 || p.Cmds[2].Depth != 1 {
		t.Fatalf("%+v", p.Cmds)
	}
	// An expansion in a word that is not the program word does not stop the reader.
	p = ReadCommand(`bash -c "cd $DIR && make"`)
	if !p.Read || !p.RuntimeText || !slices.ContainsFunc(p.Cmds, func(c SimpleCmd) bool {
		return slices.Equal(c.Argv, []string{"cd", "$DIR"}) && c.Depth == 1
	}) {
		t.Fatalf("%+v", p)
	}
}

// These commands run no inner command.
func TestWrapperNoInnerCommand(t *testing.T) {
	for _, in := range []string{"sudo -l", "sudo --list", "command -v rm", "command -V rm", "bash x.sh",
		"ssh prod", "docker ps", "sudo -i", "env", "nice", "bash -c", "su", "su - root", "sudo -e /etc/hosts",
		"flock 9", "script", "script out.log", "timeout --help", "ssh -N -L 8080:x:80 prod"} {
		if p := ReadCommand(in); !p.Read || len(p.Cmds) != 1 {
			t.Errorf("%q: read=%v why=%q cmds=%q", in, p.Read, p.Why, argvs(p))
		}
	}
}

func TestWrapperCannotRead(t *testing.T) {
	const runTime = "the program name is built at run time"
	for _, tc := range []struct{ in, why string }{
		{"sudo --us root rm x", "the command has an option that the gate does not know: sudo --us"},
		{"ssh -Z prod ls", "the command has an option that the gate does not know: ssh -Z"},
		{"ssh prod -Z ls", "the command has an option that the gate does not know: ssh -Z"},
		{"xargs --bogus rm", "the command has an option that the gate does not know: xargs --bogus"},
		{"bash --bogus -c ls", "the command has an option that the gate does not know: bash --bogus"},
		{"kubectl exec api-0 --bogus -- ls", "the command has an option that the gate does not know: kubectl --bogus"},
		{"eval {a,b}" + strings.Repeat("x", MaxCommandBytes-12), "the command is longer than 32 KB"},
		{"env -S '$X y'", `the env -S value has $, \, or #`},
		{`sh -c "$CMD"`, runTime},
		{`eval "$x"`, runTime},
		{`ssh prod "$CMD"`, runTime},
		{"sudo $CMD x", runTime},
		{`sudo "" x`, "the program name is empty"},
		{nestShells(MaxParseDepth + 1), "the command has more than 4 levels of nested shells"},
		{strings.Repeat("nice ", MaxWrapperChain+1) + "ls", "the command has more than 16 wrappers"},
	} {
		if p := ReadCommand(tc.in); p.Read || p.Why != tc.why {
			t.Errorf("%q: read=%v why=%q", tc.in, p.Read, p.Why)
		}
	}
	// The limits are not off by one.
	if p := ReadCommand(nestShells(MaxParseDepth)); !p.Read || p.Cmds[len(p.Cmds)-1].Depth != MaxParseDepth {
		t.Fatalf("%q %+v", p.Why, p.Cmds)
	}
	if p := ReadCommand(strings.Repeat("nice ", MaxWrapperChain) + "ls"); !p.Read {
		t.Fatal(p.Why)
	}
}

// A wrapper inside a substitution keeps the Via and the Depth of the path.
func TestWrapperInSubstitution(t *testing.T) {
	p := ReadCommand("sudo sh -c 'echo $(rm x)'")
	last := p.Cmds[len(p.Cmds)-1]
	if !p.Read || !slices.Equal(last.Argv, []string{"rm", "x"}) || !slices.Equal(last.Via, []string{"sudo"}) || last.Depth != 1 {
		t.Fatalf("%+v", p)
	}
}
