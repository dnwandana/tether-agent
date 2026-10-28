package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Verdict is the result of a guard scan.
type Verdict string

const (
	VerdictSafe      Verdict = "safe"
	VerdictCaution   Verdict = "caution"
	VerdictDangerous Verdict = "dangerous"
)

// Action is the guard decision for a skill.
type Action string

const (
	// ActionAllow saves or loads the skill.
	ActionAllow Action = "allow"
	// ActionBlock refuses the skill.
	ActionBlock Action = "block"
	// ActionAsk refuses the skill with the findings, so the agent can write
	// it again without the flagged content.
	ActionAsk Action = "ask"
)

// Finding is one guard hit.
type Finding struct {
	PatternID   string
	Category    string
	Description string
	File        string
	Line        int
	Severity    Severity
	// Match is the trimmed line, cut to 120 runes. Structural findings
	// give a short fact, such as "binary: .exe".
	Match string
}

// ScanResult is the verdict and the findings of one scan.
type ScanResult struct {
	Verdict  Verdict
	Findings []Finding
}

// Structural limits from Hermes. maxScanBytes also limits how much text one
// ScanDir call reads.
const (
	maxFileCount    = 50
	maxTotalSizeKB  = 1024
	maxSingleFileKB = 256
	maxScanBytes    = maxTotalSizeKB * 1024
	maxMatchRunes   = 120
)

// scriptsHint ends the guard report when a skill sends an environment
// variable out. The API call belongs in a script, where the guard does not
// see the key.
const scriptsHint = "Put the API call in a script under ~/.tether/scripts/ and make the skill run that script. " +
	"List the key in required_environment_variables. The guard does not scan ~/.tether/scripts/."

// scannableExt holds the text file types that the guard reads. Hermes skips
// other files, except SKILL.md.
var scannableExt = map[string]bool{
	".md": true, ".txt": true, ".py": true, ".sh": true, ".bash": true, ".js": true, ".ts": true, ".rb": true,
	".yaml": true, ".yml": true, ".json": true, ".toml": true, ".cfg": true, ".ini": true, ".conf": true,
	".html": true, ".css": true, ".xml": true, ".tex": true, ".r": true, ".jl": true, ".pl": true, ".php": true,
}

// binaryExt holds the binary file types that must not be in a skill.
var binaryExt = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".bin": true, ".dat": true, ".com": true,
	".msi": true, ".dmg": true, ".app": true, ".deb": true, ".rpm": true,
}

// scriptExt holds the file types that can have the executable bit.
var scriptExt = map[string]bool{".sh": true, ".bash": true, ".py": true, ".rb": true, ".pl": true}

// invisibleChars holds zero-width and direction runes that can hide text,
// in a fixed order, with their names.
var invisibleChars = []struct {
	r    rune
	name string
}{
	{'\u200b', "zero-width space"},
	{'\u200c', "zero-width non-joiner"},
	{'\u200d', "zero-width joiner"},
	{'\u2060', "word joiner"},
	{'\u2062', "invisible times"},
	{'\u2063', "invisible separator"},
	{'\u2064', "invisible plus"},
	{'\ufeff', "BOM/zero-width no-break space"},
	{'\u202a', "LTR embedding"},
	{'\u202b', "RTL embedding"},
	{'\u202c', "pop directional"},
	{'\u202d', "LTR override"},
	{'\u202e', "RTL override"},
	{'\u2066', "LTR isolate"},
	{'\u2067', "RTL isolate"},
	{'\u2068', "first strong isolate"},
	{'\u2069', "pop directional isolate"},
}

// unicodeCharName returns a readable name for an invisible rune.
func unicodeCharName(r rune) string {
	for _, c := range invisibleChars {
		if c.r == r {
			return c.name
		}
	}
	return fmt.Sprintf("U+%04X", r)
}

// isScannable reports whether the guard reads the file at the relative path.
func isScannable(rel string) bool {
	return path.Base(rel) == skillFile || scannableExt[strings.ToLower(path.Ext(rel))]
}

// Scan applies the threat patterns and the invisible-rune check to each file.
// The key of files is the relative path and the value is the content. Files
// that are not text types are skipped, the same as Hermes. Each pattern gives
// at most one finding per line.
func Scan(files map[string]string) ScanResult {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var findings []Finding
	for _, name := range names {
		rel := filepath.ToSlash(name)
		if !isScannable(rel) {
			continue
		}
		findings = append(findings, scanText(rel, files[name])...)
	}
	return ScanResult{Verdict: verdictOf(findings), Findings: findings}
}

// scanText scans the content of one file.
func scanText(rel, content string) []Finding {
	lines := strings.Split(content, "\n")
	var out []Finding
	for _, p := range ThreatPatterns {
		for i, line := range lines {
			if p.Match(line) {
				out = append(out, Finding{
					PatternID: p.ID, Category: p.Category, Description: p.Description,
					File: rel, Line: i + 1, Severity: p.Severity, Match: cutMatch(line),
				})
			}
		}
	}
	for i, line := range lines {
		for _, c := range invisibleChars {
			if strings.ContainsRune(line, c.r) {
				out = append(out, Finding{
					PatternID: "invisible_unicode", Category: "injection",
					Description: fmt.Sprintf("invisible unicode character %s (possible text hiding/injection)", c.name),
					File:        rel, Line: i + 1, Severity: SevHigh,
					Match: fmt.Sprintf("U+%04X (%s)", c.r, c.name),
				})
				break
			}
		}
	}
	return out
}

// cutMatch trims the line and cuts it to 120 runes, the same as Hermes.
func cutMatch(line string) string {
	s := strings.TrimSpace(line)
	if r := []rune(s); len(r) > maxMatchRunes {
		return string(r[:maxMatchRunes-3]) + "..."
	}
	return s
}

// verdictOf ports the Hermes rule: a critical finding gives dangerous, a high
// finding gives caution, and medium or low findings alone give safe.
func verdictOf(findings []Finding) Verdict {
	v := VerdictSafe
	for _, f := range findings {
		switch f.Severity {
		case SevCritical:
			return VerdictDangerous
		case SevHigh:
			v = VerdictCaution
		}
	}
	return v
}

// Policy returns the guard action for a trust level and a verdict. It is the
// Hermes INSTALL_POLICY table. TrustExternal uses the Hermes "trusted" row.
// An unknown trust level uses the Hermes "community" row.
func Policy(trust Trust, v Verdict) Action {
	if v == VerdictSafe {
		return ActionAllow
	}
	switch trust {
	case TrustBuiltin:
		return ActionAllow
	case TrustExternal:
		if v == VerdictCaution {
			return ActionAllow
		}
		return ActionBlock
	case TrustAgentCreated:
		if v == VerdictCaution {
			return ActionAllow
		}
		return ActionAsk
	default:
		return ActionBlock
	}
}

// envExfilIDs holds the exfiltration patterns that read an environment
// variable.
var envExfilIDs = map[string]bool{
	"env_exfil_curl": true, "env_exfil_wget": true, "env_exfil_fetch": true, "env_exfil_httpx": true,
	"env_exfil_requests": true, "encoded_exfil": true, "dump_all_env": true, "python_os_environ": true,
	"python_environ_get_secret": true, "python_getenv_secret": true, "node_process_env": true,
	"ruby_env_secret": true, "dns_exfil": true,
}

// shellVarRe finds a shell variable such as $KEY or ${KEY}.
var shellVarRe = regexp.MustCompile(`\$\{?[A-Za-z_]`)

// FormatFindings writes the verdict and the findings, critical first. When an
// exfiltration finding involves an environment variable, the text ends with
// the hint to put the API call in a script.
func FormatFindings(r ScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill guard verdict: %s (%d findings)\n", strings.ToUpper(string(r.Verdict)), len(r.Findings))
	sorted := append([]Finding(nil), r.Findings...)
	sort.SliceStable(sorted, func(i, j int) bool { return sevRank(sorted[i].Severity) < sevRank(sorted[j].Severity) })
	hint := false
	for _, f := range sorted {
		fmt.Fprintf(&b, "  %-8s %-20s %s:%d %s: %s: %q\n",
			strings.ToUpper(string(f.Severity)), f.Category, f.File, f.Line, f.PatternID, f.Description, f.Match)
		if f.Category == "exfiltration" && (envExfilIDs[f.PatternID] || shellVarRe.MatchString(f.Match)) {
			hint = true
		}
	}
	if hint {
		b.WriteString(scriptsHint)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// sevRank orders severities from critical to low.
func sevRank(s Severity) int {
	switch s {
	case SevCritical:
		return 0
	case SevHigh:
		return 1
	case SevMedium:
		return 2
	case SevLow:
		return 3
	}
	return 4
}

// errTooLargeToScan tells that a skill has more text than one scan reads.
var errTooLargeToScan = fmt.Errorf("skill has more than %d KB of text files; the guard does not scan it", maxTotalSizeKB)

// ScanDir scans all files of the skill folder dir. It adds the Hermes
// structural checks: file count, file size, binary files, executable bits,
// and symlinks. It honors .skillignore and .clawhubignore. It returns an
// error when the text files are larger than 1 MB, so no text is left
// unscanned.
func ScanDir(dir string) (ScanResult, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan skill %s: %w", dir, err)
	}
	ignore := loadSkillIgnore(root)
	var structural []Finding
	files := map[string]string{}
	count, total, scanned := 0, int64(0), 0
	add := func(f Finding) { structural = append(structural, f) }

	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		relOS, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if ignore(rel) {
			return nil
		}
		count++
		readPath := p
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				add(Finding{PatternID: "broken_symlink", Severity: SevMedium, Category: "traversal", File: rel,
					Match: "broken symlink", Description: "broken or circular symlink"})
				return nil
			}
			if !within(root, target) {
				add(Finding{PatternID: "symlink_escape", Severity: SevCritical, Category: "traversal", File: rel,
					Match: "symlink -> " + target, Description: "symlink points outside the skill directory"})
				return nil
			}
			if st, err := os.Stat(target); err != nil || !st.Mode().IsRegular() {
				return nil
			}
			readPath = target
		} else {
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			size := info.Size()
			total += size
			ext := strings.ToLower(path.Ext(rel))
			if size > maxSingleFileKB*1024 {
				add(Finding{PatternID: "oversized_file", Severity: SevMedium, Category: "structural", File: rel,
					Match:       fmt.Sprintf("%dKB", size/1024),
					Description: fmt.Sprintf("file is %dKB (limit: %dKB)", size/1024, maxSingleFileKB)})
			}
			if binaryExt[ext] {
				add(Finding{PatternID: "binary_file", Severity: SevCritical, Category: "structural", File: rel,
					Match:       "binary: " + ext,
					Description: fmt.Sprintf("binary/executable file (%s) should not be in a skill", ext)})
			}
			if !scriptExt[ext] && info.Mode().Perm()&0o111 != 0 {
				add(Finding{PatternID: "unexpected_executable", Severity: SevMedium, Category: "structural", File: rel,
					Match: "executable bit set", Description: "file has executable permission but is not a recognized script type"})
			}
		}
		if !isScannable(rel) {
			return nil
		}
		data, err := os.ReadFile(readPath)
		if err != nil {
			return nil
		}
		scanned += len(data)
		if scanned > maxScanBytes {
			return errTooLargeToScan
		}
		files[rel] = string(data)
		return nil
	})
	if err != nil {
		if errors.Is(err, errTooLargeToScan) {
			return ScanResult{}, err
		}
		return ScanResult{}, fmt.Errorf("scan skill %s: %w", dir, err)
	}
	if count > maxFileCount {
		add(Finding{PatternID: "too_many_files", Severity: SevMedium, Category: "structural", File: "(directory)",
			Match: fmt.Sprintf("%d files", count), Description: fmt.Sprintf("skill has %d files (limit: %d)", count, maxFileCount)})
	}
	if total > maxTotalSizeKB*1024 {
		add(Finding{PatternID: "oversized_skill", Severity: SevHigh, Category: "structural", File: "(directory)",
			Match:       fmt.Sprintf("%dKB total", total/1024),
			Description: fmt.Sprintf("skill is %dKB total (limit: %dKB)", total/1024, maxTotalSizeKB)})
	}
	r := Scan(files)
	all := append(structural, r.Findings...)
	return ScanResult{Verdict: verdictOf(all), Findings: all}, nil
}

// within reports whether target is root or a path under root.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// Ignore files that a skill can ship to keep development files out of the
// scan. Hermes reads both names.
var skillIgnoreFiles = []string{".skillignore", ".clawhubignore"}

// loadSkillIgnore ports the Hermes .skillignore matcher. Blank lines and "#"
// comments are skipped. A trailing "/" marks a folder. A leading "/" anchors
// a pattern to the skill root. "*" and "?" are fnmatch globs. The ignore
// files are always ignored, and SKILL.md is never ignored.
func loadSkillIgnore(dir string) func(rel string) bool {
	var patterns []string
	for _, name := range skillIgnoreFiles {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		for _, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			patterns = append(patterns, line)
		}
	}
	return func(rel string) bool {
		rel = filepath.ToSlash(rel)
		segs := strings.Split(rel, "/")
		base := segs[len(segs)-1]
		if base == skillFile {
			return false
		}
		for _, n := range skillIgnoreFiles {
			if base == n {
				return true
			}
		}
		for _, pat := range patterns {
			anchored := strings.HasPrefix(pat, "/")
			p := strings.TrimLeft(pat, "/")
			isDir := strings.HasSuffix(p, "/")
			p = strings.TrimRight(p, "/")
			if p == "" {
				continue
			}
			if isDir {
				if rel == p || strings.HasPrefix(rel, p+"/") {
					return true
				}
				if !anchored && strings.Contains("/"+rel+"/", "/"+p+"/") {
					return true
				}
				continue
			}
			if fnmatch(p, rel) {
				return true
			}
			if anchored {
				continue
			}
			if fnmatch(p, base) {
				return true
			}
			if !strings.Contains(p, "/") {
				for _, s := range segs {
					if fnmatch(p, s) {
						return true
					}
				}
			}
			if strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return false
	}
}

// fnmatch ports Python fnmatch.fnmatch on POSIX. Unlike path.Match, "*"
// also matches "/".
func fnmatch(pattern, name string) bool {
	pr := []rune(pattern)
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	for i := 0; i < len(pr); i++ {
		c := pr[i]
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '[':
			j := i + 1
			if j < len(pr) && pr[j] == '!' {
				j++
			}
			if j < len(pr) && pr[j] == ']' {
				j++
			}
			for j < len(pr) && pr[j] != ']' {
				j++
			}
			if j >= len(pr) {
				b.WriteString(`\[`)
				continue
			}
			set := string(pr[i+1 : j])
			neg := strings.HasPrefix(set, "!")
			set = strings.TrimPrefix(set, "!")
			set = strings.ReplaceAll(set, `\`, `\\`)
			if neg {
				b.WriteString("[^")
			} else {
				b.WriteString("[")
			}
			b.WriteString(set)
			b.WriteString("]")
			i = j
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(name)
}
