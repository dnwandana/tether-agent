package skills

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// UsageError is a bad command line for a skills command. The CLI maps it to
// exit code 2.
type UsageError struct{ Msg string }

// Error returns the message of the usage error.
func (e *UsageError) Error() string { return e.Msg }

// usagef returns a *UsageError with a formatted message.
func usagef(format string, args ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, args...)}
}

// SourceKind tells where an install source comes from.
type SourceKind int

const (
	// SourceLocal is a folder on this machine.
	SourceLocal SourceKind = iota + 1
	// SourceGit is a git remote.
	SourceGit
	// SourceIndex is a name in the curated index.
	SourceIndex
)

// Source is a parsed install source.
type Source struct {
	Kind SourceKind
	// Identifier is the source as the user typed it.
	Identifier string
	// URL is the clone URL, or the absolute path of a local folder.
	URL string
	// Ref is the ref of a tree URL, or the value of --ref.
	Ref string
	// Path is the search folder inside the repo of a tree URL, else "".
	Path string
	// IndexName is the name to look up in the curated index.
	IndexName string
}

// SourceOptions holds the install flags that change how a source parses.
type SourceOptions struct {
	Ref   string
	Skill string
}

// shorthandRe matches the GitHub shorthand owner/repo.
var shorthandRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// repoPartRe matches one GitHub owner or repo name.
var repoPartRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// gitSchemes are the URL schemes that a git source can use. They are the
// protocols that the git runner allows. Other schemes, for example "ext" and
// "file", can run commands or read local files, and "http" has no TLS.
var gitSchemes = map[string]bool{"https": true, "ssh": true, "git": true}

// trustedRepos is the Hermes TRUSTED_REPOS set, in lower case.
var trustedRepos = map[string]bool{
	"openai/skills":      true,
	"anthropics/skills":  true,
	"huggingface/skills": true,
	"nvidia/skills":      true,
}

// sourceForms names the source forms that install accepts.
const sourceForms = "Give a local folder (./dir, /dir, or ~/dir), a git URL, " +
	"a GitHub tree URL, owner/repo, or a skill name from the index."

// ParseSource parses the source argument of install. The first rule that
// matches wins: a local folder, a git URL, the GitHub shorthand owner/repo,
// and then a name in the curated index. It does not touch the disk or the
// network.
func ParseSource(arg string, opts SourceOptions) (Source, error) {
	if strings.HasPrefix(arg, "-") {
		return Source{}, usagef("the source %q starts with \"-\". %s", arg, sourceForms)
	}
	if strings.HasPrefix(opts.Ref, "-") {
		return Source{}, usagef("the ref %q starts with \"-\"", opts.Ref)
	}
	opts.Ref = normalizeRef(opts.Ref)
	switch {
	case strings.HasPrefix(arg, ".") || strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~"):
		return parseLocal(arg, opts)
	case strings.HasPrefix(arg, "git@") || strings.Contains(arg, "://"):
		return parseGitURL(arg, opts)
	case shorthandRe.MatchString(arg):
		return Source{Kind: SourceGit, Identifier: arg, URL: "https://github.com/" + arg, Ref: opts.Ref}, nil
	case validNameRe.MatchString(arg):
		if opts.Ref != "" || opts.Skill != "" {
			return Source{}, usagef("--ref and --skill cannot be used with an index name")
		}
		return Source{Kind: SourceIndex, Identifier: arg, IndexName: arg}, nil
	}
	return Source{}, usagef("the source %q has no known form. %s", arg, sourceForms)
}

// normalizeRef returns a commit SHA in lower case and every other ref as it
// is. Git accepts a SHA in any case, but IsCommitSHA, the lockfile, and the
// pinned check compare lower case only.
func normalizeRef(ref string) string {
	if lower := strings.ToLower(ref); IsCommitSHA(lower) {
		return lower
	}
	return ref
}

// parseLocal parses a local folder. The folder does not need to exist yet.
func parseLocal(arg string, opts SourceOptions) (Source, error) {
	if opts.Ref != "" {
		return Source{}, usagef("--ref cannot be used with a local folder")
	}
	abs, err := filepath.Abs(expandHome(arg))
	if err != nil {
		return Source{}, usagef("the local folder %q has no absolute path: %v", arg, err)
	}
	return Source{Kind: SourceLocal, Identifier: arg, URL: abs}, nil
}

// parseGitURL parses a git URL and a GitHub tree URL.
func parseGitURL(arg string, opts SourceOptions) (Source, error) {
	if !strings.HasPrefix(arg, "git@") {
		scheme, _, _ := strings.Cut(arg, "://")
		if !gitSchemes[strings.ToLower(scheme)] {
			return Source{}, usagef("the URL scheme %q is not supported. Use https, ssh, or git.", scheme)
		}
	}
	src := Source{Kind: SourceGit, Identifier: arg, URL: arg, Ref: opts.Ref}
	owner, repo, ref, sub, isTree, err := parseTreeURL(arg)
	if err != nil {
		return Source{}, err
	}
	if !isTree {
		return src, nil
	}
	if opts.Ref != "" {
		return Source{}, usagef("--ref cannot be used with a tree URL, because the URL gives the ref")
	}
	src.URL = "https://github.com/" + owner + "/" + repo
	src.Ref = normalizeRef(ref)
	src.Path = sub
	return src, nil
}

// parseTreeURL reads https://github.com/<o>/<r>/tree/<ref>/<path>. It
// returns isTree false for every other URL.
func parseTreeURL(arg string) (owner, repo, ref, sub string, isTree bool, err error) {
	u, perr := url.Parse(arg)
	if perr != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Host, "github.com") {
		return "", "", "", "", false, nil
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || parts[2] != "tree" {
		return "", "", "", "", false, nil
	}
	owner, repo = parts[0], parts[1]
	if !repoPartRe.MatchString(owner) || !repoPartRe.MatchString(repo) {
		return "", "", "", "", false, usagef("the tree URL %q has no valid owner/repo", arg)
	}
	if len(parts) < 4 || parts[3] == "" {
		return "", "", "", "", false, usagef("the tree URL %q gives no ref after tree/", arg)
	}
	ref = parts[3]
	if strings.HasPrefix(ref, "-") {
		return "", "", "", "", false, usagef("the ref %q starts with \"-\"", ref)
	}
	var keep []string
	for _, p := range parts[4:] {
		switch p {
		case "":
			continue
		case ".", "..":
			return "", "", "", "", false, usagef("the tree URL %q has a %q segment in its path", arg, p)
		}
		keep = append(keep, p)
	}
	return owner, repo, ref, strings.Join(keep, "/"), true, nil
}

// GitHubRepo returns "<owner>/<repo>" for a GitHub URL of the form
// https://github.com/<o>/<r>[.git][/] or git@github.com:<o>/<r>[.git].
// The host must be exactly github.com, in any case. The path must have
// exactly two segments, with no "." or ".." segment and no percent escape,
// because git resolves those and could fetch a repo other than the one that
// gives the trust level.
func GitHubRepo(rawURL string) (ownerRepo string, ok bool) {
	if strings.Contains(rawURL, "%") {
		return "", false
	}
	var parts []string
	if rest, found := strings.CutPrefix(rawURL, "git@"); found {
		host, p, hasColon := strings.Cut(rest, ":")
		if !hasColon || !strings.EqualFold(host, "github.com") {
			return "", false
		}
		parts = strings.Split(p, "/")
		if len(parts) != 2 {
			return "", false
		}
	} else {
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(rawURL, "#") {
			return "", false
		}
		parts = strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
		if len(parts) != 2 {
			return "", false
		}
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	for _, p := range []string{owner, repo} {
		if p == "." || p == ".." || !repoPartRe.MatchString(p) {
			return "", false
		}
	}
	return owner + "/" + repo, true
}

// TrustForURL returns TrustTrusted for a GitHub repo in trustedRepos, and
// TrustCommunity for every other source, a local folder too.
func TrustForURL(rawURL string) Trust {
	if repo, ok := GitHubRepo(rawURL); ok && trustedRepos[strings.ToLower(repo)] {
		return TrustTrusted
	}
	return TrustCommunity
}
