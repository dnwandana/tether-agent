package security

import (
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// envTemplateNames are the env files that hold no secret by convention.
var envTemplateNames = []string{".env.example", ".env.sample", ".env.template", ".env.dist"}

// credentialSuffixes are the file name endings of key and key store files.
var credentialSuffixes = []string{".pem", ".key", ".p12", ".pfx", ".keystore", ".jks"}

// credentialNames are the exact file names of SSH keys and login files.
var credentialNames = []string{
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	".netrc", ".pgpass", ".npmrc", ".pypirc", ".git-credentials",
	"credentials.json",
}

// credentialPrefixes are the starts of cloud key file names. The file must also end in ".json".
var credentialPrefixes = []string{"service-account", "client_secret"}

// credentialFolders are the folders whose files all hold credentials.
var credentialFolders = append([]string{".ssh"}, homeCredentialDirs...)

// IsCredentialFile reports whether path names a file that holds credentials: an env file, a
// key or key store, an SSH key, a login file, a cloud key, or a file in a credential folder.
// path can be relative. The check uses the base name, and the path for the folders.
func IsCredentialFile(p string) bool {
	p = filepath.ToSlash(p)
	base := path.Base(p)
	if base == "." || base == "/" {
		return false
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return !slices.Contains(envTemplateNames, base)
	}
	for _, s := range credentialSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	if slices.Contains(credentialNames, base) {
		return true
	}
	if strings.HasSuffix(base, ".json") {
		for _, pre := range credentialPrefixes {
			if strings.HasPrefix(base, pre) {
				return true
			}
		}
	}
	// A folder entry matches only when the path goes on after the folder.
	full := "/" + strings.TrimPrefix(p, "/")
	for _, d := range credentialFolders {
		if strings.Contains(full, "/"+d+"/") {
			return true
		}
	}
	return false
}
