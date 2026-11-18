package security

import (
	"strings"
	"testing"
)

// humanOnly returns the ID of the human-only rule that cmd matches, or "".
func humanOnly(cmd string) string {
	if p, ok := MatchHumanOnlyParsed(cmd, ReadCommand(cmd)); ok {
		return p.ID
	}
	return ""
}

func TestHumanOnlyPatternIDs(t *testing.T) {
	want := map[string]string{
		"human_only_password_change":    "password change",
		"human_only_account_creation":   "account creation",
		"human_only_credential_access":  "new credential access",
		"human_only_permanent_deletion": "permanent deletion",
	}
	if len(HumanOnlyPatterns) != len(want) {
		t.Fatal(len(HumanOnlyPatterns))
	}
	for _, p := range HumanOnlyPatterns {
		if want[p.ID] != p.Description {
			t.Fatal(p.ID, p.Description)
		}
	}
}

func TestHumanOnlyShellRules(t *testing.T) {
	const (
		pw   = "human_only_password_change"
		acct = "human_only_account_creation"
		cred = "human_only_credential_access"
		del  = "human_only_permanent_deletion"
	)
	for cmd, want := range map[string]string{
		"passwd":                                       pw,
		"passwd bob":                                   pw,
		"sudo passwd root":                             pw,
		"/usr/bin/passwd bob":                          pw,
		"echo 'u:pw' | chpasswd":                       pw,
		"usermod -p HASH bob":                          pw,
		"usermod --password=HASH bob":                  pw,
		"smbpasswd -a bob":                             pw,
		"htpasswd -b .htpasswd bob pw":                 pw,
		"useradd -m eve":                               acct,
		"adduser eve":                                  acct,
		"usermod -aG sudo eve":                         acct,
		"usermod -G wheel,video eve":                   acct,
		"usermod --groups=docker eve":                  acct,
		"gpasswd -a eve docker":                        acct,
		"ssh-copy-id prod":                             cred,
		"cat key.pub >> ~/.ssh/authorized_keys":        cred,
		"echo key | tee -a /root/.ssh/authorized_keys": cred,
		"gh auth login":                                cred,
		"gh auth refresh -s repo":                      cred,
		"gcloud auth login":                            cred,
		"aws configure":                                cred,
		"az login":                                     cred,
		"docker login ghcr.io":                         cred,
		"npm login":                                    cred,
		"npm adduser":                                  cred,
		"sh -c 'ssh-copy-id prod'":                     cred,
		"shred -u secret.txt":                          del,
		"wipefs -a /dev/sdb":                           del,
		"srm -r old":                                   del,
		"blkdiscard /dev/nvme0n1":                      del,
		"dd if=/dev/zero of=disk.img bs=1M count=10":   del,
		"dd if=/dev/urandom of=/home/u/file":           del,
		// Near misses.
		"cat /etc/passwd":                  "",
		"getent passwd":                    "",
		"grep root /etc/passwd":            "",
		"htpasswd -c .htpasswd bob":        "",
		"usermod -L bob":                   "",
		"usermod -aG video eve":            "",
		"gpasswd -a eve video":             "",
		`git commit -m "doc: ssh-copy-id"`: "",
		"man passwd":                       "",
		"cat ~/.ssh/authorized_keys":       "",
		"gh auth status":                   "",
		"docker logout":                    "",
		"aws s3 ls":                        "",
		"dd if=disk.img of=copy.img":       "",
		"dd if=/dev/zero of=/dev/sda":      "",
		"echo shred":                       "",
		"rm old.txt":                       "",
	} {
		if got := humanOnly(cmd); got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
}

func TestHumanOnlyRawWhenUnread(t *testing.T) {
	for cmd, want := range map[string]string{
		`passwd bob; echo "x`:            "human_only_password_change",
		`sudo useradd eve; echo "x`:      "human_only_account_creation",
		`x=1 && shred f; echo "x`:        "human_only_permanent_deletion",
		`cat /etc/passwd; echo "x`:       "",
		`grep root /etc/passwd; echo "x`: "",
	} {
		if p := ReadCommand(cmd); p.Read {
			t.Fatal("the test command must be unreadable:", cmd)
		}
		if got := humanOnly(cmd); got != want {
			t.Errorf("%q: got %q, want %q", cmd, got, want)
		}
	}
}

func TestHumanOnlyScript(t *testing.T) {
	for code, want := range map[string]string{
		`subprocess.run(["passwd", "bob"])`:       "human_only_password_change",
		`os.system("useradd -m eve")`:             "human_only_account_creation",
		`subprocess.run(['ssh-copy-id', 'prod'])`: "human_only_credential_access",
		`os.system("gh auth login")`:              "human_only_credential_access",
		`os.system('shred -u f')`:                 "human_only_permanent_deletion",
		`open("/etc/passwd").read()`:              "",
		`print("run passwd later")`:               "",
	} {
		got := ""
		if p, ok := MatchHumanOnlyScript(code); ok {
			got = p.ID
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", code, got, want)
		}
	}
}

func TestHumanOnlyRaiseList(t *testing.T) {
	t.Cleanup(func() { SetHumanOnlyRules(nil) })
	SetHumanOnlyRules([]string{"git_force_push", "mcp:a.b.c"})
	if !IsHumanOnlyRaised("git_force_push") || IsHumanOnlyRaised("recursive_delete") || IsHumanOnlyRaised("mcp:a.b.c") {
		t.Fatal("rule raise")
	}
	// The entry matches as a whole string, so both splits of "a.b.c" match.
	if !IsHumanOnlyMCP("a.b", "c") || !IsHumanOnlyMCP("a", "b.c") || IsHumanOnlyMCP("a", "b") {
		t.Fatal("MCP raise")
	}
	SetHumanOnlyRules(nil)
	if IsHumanOnlyRaised("git_force_push") || IsHumanOnlyMCP("a.b", "c") {
		t.Fatal("SetHumanOnlyRules(nil) must clear the list")
	}
}

func TestCheckHumanOnly(t *testing.T) {
	if err := CheckHumanOnly(nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckHumanOnly([]string{"git_force_push", "sqlite_client_state_db", "mcp:stripe.create_refund"}); err != nil {
		t.Fatal(err)
	}
	err := CheckHumanOnly([]string{"git_force_push", "no_such_rule"})
	if err == nil || !strings.Contains(err.Error(), "no_such_rule") {
		t.Fatal(err)
	}
	err = CheckHumanOnly([]string{"recursive_delete_root_filesystem"})
	if err == nil || !strings.Contains(err.Error(), "recursive_delete_root_filesystem") ||
		!strings.Contains(err.Error(), "hardline rules are already blocked") {
		t.Fatal(err)
	}
}
