package security

import (
	"path"
	"strings"
)

// optValueFlags are the options of ssh, sftp, scp, and nc that take a value.
var optValueFlags = map[string]bool{
	"-p": true, "-P": true, "-i": true, "-l": true, "-o": true, "-F": true, "-J": true,
	"-L": true, "-R": true, "-D": true, "-b": true, "-c": true, "-E": true, "-e": true,
	"-m": true, "-S": true, "-W": true, "-w": true,
}

// IsNetworkProgram reports whether the simple command c reads from the network. program is
// the program name, or two words for a git or gh subcommand. host is the lower-case host of
// the target, or "" when the command does not name one or the name is not a valid host.
func IsNetworkProgram(c SimpleCmd) (program, host string, ok bool) {
	if len(c.Argv) == 0 {
		return "", "", false
	}
	name := path.Base(c.Argv[0])
	args := c.Argv[1:]
	switch name {
	case "curl", "wget", "lynx", "w3m", "links", "aria2c":
		for _, a := range args {
			if strings.Contains(a, "://") {
				return name, urlHost(a), true
			}
		}
		return name, "", true
	case "http", "https", "xh":
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if strings.Contains(a, "://") {
				return name, urlHost(a), true
			}
			if strings.Contains(a, ".") {
				return name, cleanHost(a), true
			}
		}
		return name, "", true
	case "nc", "ncat":
		return name, cleanHost(firstOperand(args, true)), true
	case "ftp":
		return name, cleanHost(firstOperand(args, false)), true
	case "socat":
		for _, a := range args {
			up := strings.ToUpper(a)
			for _, p := range []string{"TCP4:", "TCP6:", "TCP:", "SSL:", "OPENSSL:"} {
				if strings.HasPrefix(up, p) {
					return "socat", cleanHost(a[len(p):]), true
				}
			}
		}
		return "socat", "", true
	case "ssh", "sftp":
		return name, cleanHost(firstOperand(args, true)), true
	case "scp":
		for i := 0; i < len(args); i++ {
			a := args[i]
			if strings.HasPrefix(a, "-") {
				if optValueFlags[a] {
					i++
				}
				continue
			}
			if h, found := remoteHost(a); found {
				return "scp", h, true
			}
		}
		return "scp", "", true
	case "rsync":
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if strings.HasPrefix(a, "rsync://") {
				return "rsync", urlHost(a), true
			}
			if h, found := remoteHost(a); found {
				return "rsync", h, true
			}
		}
		return "", "", false
	case "git":
		return gitNetwork(args)
	case "gh":
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if a == "api" {
				return "gh api", "api.github.com", true
			}
			break
		}
	}
	return "", "", false
}

// gitNetwork handles git clone, fetch, and pull. It skips the global options -C and -c.
func gitNetwork(args []string) (string, string, bool) {
	i := 0
	for i < len(args) {
		if args[i] == "-C" || args[i] == "-c" {
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			i++
			continue
		}
		break
	}
	if i >= len(args) {
		return "", "", false
	}
	sub := args[i]
	if sub != "clone" && sub != "fetch" && sub != "pull" {
		return "", "", false
	}
	program := "git " + sub
	for _, a := range args[i+1:] {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if strings.Contains(a, "://") {
			return program, urlHost(a), true
		}
		if at := strings.Index(a, "@"); at > 0 && strings.Contains(a[at:], ":") {
			return program, cleanHost(a), true
		}
	}
	return program, "", true
}

// firstOperand returns the first argument that is not a flag. When skipValues is true, it
// skips the value of an option that takes one.
func firstOperand(args []string, skipValues bool) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if skipValues && optValueFlags[a] {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// remoteHost reads a [user@]host:path argument. found is false for a local path.
func remoteHost(a string) (host string, found bool) {
	i := strings.Index(a, ":")
	if i <= 0 || strings.Contains(a[:i], "/") {
		return "", false
	}
	return cleanHost(a[:i]), true
}

// urlHost returns the clean host of a URL with a scheme.
func urlHost(u string) string {
	_, rest, ok := strings.Cut(u, "://")
	if !ok {
		return ""
	}
	return cleanHost(rest)
}

// cleanHost cuts a user@ head, a :port tail, a path, and a trailing dot, and makes the text
// lower case. It returns "" for a text that is not 1 to 253 characters of [a-z0-9.-].
func cleanHost(s string) string {
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if s == "" || len(s) > 253 {
		return ""
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return ""
		}
	}
	return s
}
