package security

import "testing"

func TestIsCredentialFile(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{".env", true},
		{"deploy/.env.production", true},
		{".env.local", true},
		{".env.example", false},
		{".env.sample", false},
		{".env.template", false},
		{"config/.env.dist", false},
		{"deploy/server.pem", true},
		{"tls/site.key", true},
		{"cert.p12", true},
		{"cert.pfx", true},
		{"android/release.keystore", true},
		{"java/store.jks", true},
		{"id_rsa", true},
		{"backup/id_ed25519", true},
		{"id_ecdsa", true},
		{"id_dsa", true},
		{"id_rsa.pub", false},
		{".netrc", true},
		{".pgpass", true},
		{".npmrc", true},
		{".pypirc", true},
		{".git-credentials", true},
		{"credentials.json", true},
		{"gcp/service-account-prod.json", true},
		{"client_secret_123.apps.googleusercontent.com.json", true},
		{"home/.ssh/config", true},
		{"home/.ssh/known_hosts", true},
		{".aws/credentials", true},
		{"/home/user/.aws/config", true},
		{".gnupg/pubring.kbx", true},
		{".kube/config", true},
		{".docker/config.json", true},
		{".azure/msal_token_cache.json", true},
		{".config/gh/hosts.yml", true},
		{".config/gcloud/application_default_credentials.json", true},
		{".config/git/config", false},
		{".config/systemd/user/x.service", false},
		{"main.go", false},
		{"README.md", false},
		{"docs/keys.md", false},
		{"package.json", false},
		{"env.go", false},
		{".environment", false},
		{".aws", false},
	} {
		if got := IsCredentialFile(tc.path); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.path, got, tc.want)
		}
	}
}

// The credential folders are still protected paths.
func TestCredentialDirsStayProtected(t *testing.T) {
	got := ProtectedPaths("", "/home/u")
	for _, want := range []string{"/home/u/.aws/", "/home/u/.config/gcloud/", "/home/u/.config/systemd/"} {
		found := false
		for _, p := range got {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no protected path %q in %v", want, got)
		}
	}
}
