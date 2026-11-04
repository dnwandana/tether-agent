package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	// Unscanned holds the sorted relative paths of the files that ScanDir
	// did not pattern-scan, because their content is not text.
	Unscanned []string
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

// mediaExt holds the file types that the guard skips when they hold a NUL
// byte: images, fonts, documents, archives, audio, video, and other
// compressed or model files that can start with no NUL byte.
var mediaExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".ico": true,
	".tif": true, ".tiff": true, ".avif": true, ".heic": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true, ".odt": true,
	".zip": true, ".gz": true, ".tgz": true, ".tar": true, ".bz2": true, ".xz": true, ".7z": true,
	".mp3": true, ".wav": true, ".ogg": true, ".flac": true, ".m4a": true, ".mp4": true, ".webm": true, ".mov": true,
	".zst": true, ".br": true, ".lz4": true, ".lzma": true, ".onnx": true,
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

// InvisibleRune returns the first rune of s that is in the list of invisible
// characters of the skill guard, with its name from that list. ok is false when
// s has no such rune. The memory guard uses it, so both guards share one list.
func InvisibleRune(s string) (r rune, name string, ok bool) {
	for _, r := range s {
		for _, c := range invisibleChars {
			if c.r == r {
				return c.r, c.name, true
			}
		}
	}
	return 0, "", false
}

// isScannable reports whether the file at the relative path is a text type
// that the guard always reads, whatever its bytes are.
func isScannable(rel string) bool {
	return path.Base(rel) == skillFile || scannableExt[strings.ToLower(path.Ext(rel))]
}

// bashSampleBytes is the number of bytes that bash checks before it runs a
// file as a script.
const bashSampleBytes = 80

// mustScan reports whether the guard reads the file at the relative path when
// its content has a NUL byte. head is the start of the content. SKILL.md, the
// scannableExt types, each file under scripts/, and each file that starts
// with "#!" are always read. Another file is read when bash can run it: its
// type is not in mediaExt, it is not under .git/, and its first line in the
// first 80 bytes has no NUL byte. Bash 3.2 and later refuse a file only when
// this test fails, and bash deletes a NUL byte after the test.
func mustScan(rel string, head []byte) bool {
	if isScannable(rel) || strings.HasPrefix(rel, "scripts/") || bytes.HasPrefix(head, []byte("#!")) {
		return true
	}
	if mediaExt[strings.ToLower(path.Ext(rel))] || strings.HasPrefix(rel, ".git/") || strings.Contains(rel, "/.git/") {
		return false
	}
	first := head[:min(len(head), bashSampleBytes)]
	if i := bytes.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	return bytes.IndexByte(first, 0) < 0
}

// Scan applies the threat patterns and the invisible-rune check to each file.
// The key of files is the relative path and the value is the content. The
// guard scans every file, whatever its extension, because a script with no
// extension or with an unknown extension can run too. Each pattern gives at
// most one finding per line. It deletes each NUL byte before the scan, as
// bash and dash do.
func Scan(files map[string]string) ScanResult {
	return scanFiles(files, nil)
}

// scanFiles scans the files in name order. A nil keep scans every file.
func scanFiles(files map[string]string, keep func(rel string) bool) ScanResult {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var findings []Finding
	for _, name := range names {
		rel := filepath.ToSlash(name)
		if keep != nil && !keep(rel) {
			continue
		}
		content := files[name]
		hadNUL := strings.IndexByte(content, 0) >= 0
		if hadNUL {
			content = strings.ReplaceAll(content, "\x00", "")
		}
		// Only a file that the bash rule reads skips the invisible check. SKILL.md, the
		// scannableExt types, and the scripts/ files keep it, so one NUL byte cannot hide
		// a zero-width rune in text that the model reads.
		binary := hadNUL && !isScannable(rel) && !strings.HasPrefix(rel, "scripts/")
		findings = append(findings, scanText(rel, content, binary)...)
	}
	return ScanResult{Verdict: verdictOf(findings), Findings: findings}
}

// scanText scans the content of one file. content has no NUL bytes. hadNUL
// is true for a file with a NUL byte that only the bash rule of mustScan reads.
func scanText(rel, content string, hadNUL bool) []Finding {
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
	out = append(out, scanContinued(rel, lines, out)...)
	// A file that had a NUL byte gets no invisible_unicode check, because
	// random bytes give false findings.
	if hadNUL {
		return out
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

// scanContinued applies the threat patterns to each logical line that a
// shell builds from two or more lines: a line that ends in a backslash, or a
// line that ends in "|" or "&&" (see pipeNext). A pattern that already matched
// one of the parts gives no second finding. The finding has the line number of
// the first part.
func scanContinued(rel string, lines []string, found []Finding) []Finding {
	have := map[string]bool{}
	for _, f := range found {
		have[fmt.Sprintf("%s:%d", f.PatternID, f.Line)] = true
	}
	code := codeLines(rel, lines)
	var out []Finding
	for i := 0; i < len(lines); {
		j := i
		var b strings.Builder
		for {
			part := strings.TrimSuffix(lines[j], "\r")
			if strings.HasSuffix(part, "\\") && j < len(lines)-1 {
				b.WriteString(strings.TrimSuffix(part, "\\"))
				j++
				continue
			}
			if head, next, ok := pipeNext(lines, code, j); ok {
				b.WriteString(head)
				b.WriteByte(' ')
				j = next
				continue
			}
			b.WriteString(part)
			break
		}
		if j > i {
			joined := b.String()
			for _, p := range ThreatPatterns {
				if !p.Match(joined) {
					continue
				}
				seen := false
				for k := i; k <= j; k++ {
					if have[fmt.Sprintf("%s:%d", p.ID, k+1)] {
						seen = true
						break
					}
				}
				if !seen {
					out = append(out, Finding{
						PatternID: p.ID, Category: p.Category, Description: p.Description,
						File: rel, Line: i + 1, Severity: p.Severity, Match: cutMatch(joined),
					})
				}
			}
		}
		i = j + 1
	}
	return out
}

// codeLines reports for each line whether a "|" or "&&" join can start or end
// on it. In a Markdown file, only the lines inside a fenced code block can,
// because SKILL.md gives commands in such blocks and a table row also ends in
// "|". In other files, every line can.
func codeLines(rel string, lines []string) []bool {
	code := make([]bool, len(lines))
	md := strings.ToLower(path.Ext(rel)) == ".md"
	in := false
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if md && (strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")) {
			in = !in
			continue
		}
		code[i] = !md || in
	}
	return code
}

// pipeNext returns line j without its trailing comment and spaces, and the
// index of the next command line, when line j ends in "|" or "&&". It skips
// blank lines and comment lines, as bash does. It does not join after "||",
// because the next command does not get the output, or a line that starts
// with "|", because that is a table row.
func pipeNext(lines []string, code []bool, j int) (head string, next int, ok bool) {
	part := strings.TrimSuffix(lines[j], "\r")
	if !code[j] || strings.HasPrefix(strings.TrimLeft(part, " \t"), "|") {
		return "", 0, false
	}
	head = strings.TrimRight(stripComment(part), " \t")
	if strings.HasSuffix(head, "||") || !(strings.HasSuffix(head, "|") || strings.HasSuffix(head, "&&")) {
		return "", 0, false
	}
	for k := j + 1; k < len(lines); k++ {
		s := strings.TrimSpace(lines[k])
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if !code[k] || strings.HasPrefix(s, "|") {
			return "", 0, false
		}
		return head, k, true
	}
	return "", 0, false
}

// stripComment removes a shell comment from line. A "#" starts a comment only
// at the start of the line or after a space or a tab, so a URL fragment stays.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
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
	case TrustTrusted, TrustExternal:
		if v == VerdictCaution {
			return ActionAllow
		}
		return ActionBlock
	case TrustAgentCreated:
		if v == VerdictCaution {
			return ActionAllow
		}
		return ActionAsk
	case TrustCommunity:
		return ActionBlock
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
// the hint to put the API call in a script. When the scan did not read a
// file, a "not scanned (binary)" line follows the findings.
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
	if len(r.Unscanned) > 0 {
		fmt.Fprintf(&b, "not scanned (binary): %s\n", strings.Join(r.Unscanned, ", "))
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

// errTooLargeToScan tells that a skill has more bytes to scan than one scan reads.
var errTooLargeToScan = fmt.Errorf("skill has more than %d KB of files to scan; the guard does not scan it", maxTotalSizeKB)

// ScanDir scans all files of the skill folder dir. It adds the Hermes
// structural checks: file count, file size, binary files, executable bits,
// and symlinks. It honors no ignore file: an ignore file in the folder comes
// from the author of the skill, so it must not hide a file from the guard.
// It pattern-scans SKILL.md, every scannableExt file, every file under
// scripts/, every file that starts with "#!", every other file with no NUL
// byte, and each file with a NUL byte that bash can run (mustScan). It lists
// the other files in Unscanned, stops at a file that it cannot read, and adds
// a binary_file finding for each file that starts with an executable magic
// number. More than 1 MB to scan gives errTooLargeToScan, so no file that the
// guard must read is left unscanned.
func ScanDir(dir string) (ScanResult, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ScanResult{}, fmt.Errorf("scan skill %s: %w", dir, err)
	}
	var structural []Finding
	var unscanned []string
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
		data, complete, err := readHead(readPath, maxScanBytes-scanned)
		if err != nil {
			// A file that the guard cannot read must not pass as safe.
			return fmt.Errorf("read %s: %w", rel, err)
		}
		if format, ok := executableMagic(data); ok {
			add(Finding{PatternID: "binary_file", Severity: SevCritical, Category: "structural", File: rel,
				Match:       "binary: " + format,
				Description: fmt.Sprintf("binary/executable file (%s) should not be in a skill", format)})
		}
		if !mustScan(rel, data) && !looksText(data) {
			unscanned = append(unscanned, rel)
			return nil
		}
		if !complete {
			return errTooLargeToScan
		}
		scanned += len(data)
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
	r := scanFiles(files, nil)
	sort.Strings(unscanned)
	all := append(structural, r.Findings...)
	return ScanResult{Verdict: verdictOf(all), Findings: all, Unscanned: unscanned}, nil
}

// readHead reads at most budget bytes of text from the file at p. It reads
// one byte more than budget, so a file that does not fit stops the read
// early and gives complete false. It reads at least bashSampleBytes bytes, so
// that executableMagic and mustScan always get the head of a file, also when
// the budget is almost used up.
func readHead(p string, budget int) (data []byte, complete bool, err error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	limit := max(budget, bashSampleBytes) + 1
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return nil, false, err
	}
	return data, len(data) <= budget, nil
}

// looksText reports whether data is text: it has no NUL byte. Invalid UTF-8
// does not make a file binary, because a shell or an interpreter still runs
// such a file and the patterns still match its valid parts.
func looksText(data []byte) bool {
	return bytes.IndexByte(data, 0) < 0
}

// executableMagic returns the executable format whose magic number starts
// head: ELF, PE, or one of the Mach-O forms.
func executableMagic(head []byte) (format string, ok bool) {
	switch {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "ELF", true
	case bytes.HasPrefix(head, []byte("MZ")):
		return "PE", true
	}
	for _, m := range machOMagics {
		if bytes.HasPrefix(head, []byte(m)) {
			return "Mach-O", true
		}
	}
	return "", false
}

// machOMagics holds the 32-bit and 64-bit Mach-O magic numbers in both byte
// orders, and the magic number of a fat (universal) binary.
var machOMagics = []string{"\xfe\xed\xfa\xce", "\xfe\xed\xfa\xcf", "\xce\xfa\xed\xfe", "\xcf\xfa\xed\xfe", "\xca\xfe\xba\xbe"}

// within reports whether target is root or a path under root.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
