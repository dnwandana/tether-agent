package tools

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/dnwandana/tether-agent/internal/security"
)

// maxStagedPaths is the largest count of credential paths that StagedCredentials returns.
const maxStagedPaths = 20

// stageTimeout limits the git check of a broad git add. Tests change it.
var stageTimeout = 5 * time.Second

// maxHashBytes is the largest file that the check hashes. A larger file counts as changed.
// Tests change it.
var maxHashBytes int64 = 32 << 20

// gitRun runs git with args in dir. It returns the standard output, or the standard error
// when git fails. Tests replace it.
var gitRun = func(ctx context.Context, git, dir string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, git, args...)
	cmd.Dir = dir
	cmd.Env = security.ChildEnv(os.Environ(), nil, nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// safeGitArgs come before each git command of the check. The check runs only commands that
// read the index, the config, the ignore files, and the folders. These commands never read
// the content of a file, so git starts no filter driver, and these options stop the other
// programs of the repository config.
var safeGitArgs = []string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
	"-c", "core.untrackedCache=false"}

// systemGit returns /usr/bin/git, else /bin/git, and false when neither exists. It never
// uses the PATH of the session, because the agent can change that PATH.
func systemGit() (string, bool) {
	for _, p := range []string{"/usr/bin/git", "/bin/git"} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, true
		}
	}
	return "", false
}

// gitEnvAssignRe finds a shell assignment of a variable that changes the repository, the
// index, or the config that git reads.
var gitEnvAssignRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(?:GIT_[A-Za-z0-9_]*|HOME|XDG_CONFIG_HOME)[+:]?=`)

// gitEnvNameRe matches a word that names such a variable, as in "export GIT_DIR" or
// "unset HOME".
var gitEnvNameRe = regexp.MustCompile(`^(?:GIT_[A-Za-z0-9_]*|HOME|XDG_CONFIG_HOME)(?:$|[+:]?=)`)

// stageCheck holds the state of one StagedCredentials call.
type stageCheck struct {
	ctx      context.Context
	git      string
	gitOK    bool
	gitTried bool
	// writes is true after a part that can write a file. mkdirs is true after a part that can
	// also make a folder. A redirect makes only a file.
	writes, mkdirs bool
	// envSet is true when the command sets a variable of gitEnvNameRe.
	envSet bool
}

// StagedCredentials returns the credential files that each broad git add part of p would
// stage. unchecked is true when the check could not run for a part. The check lists the
// untracked files and the index with git ls-files. For a tracked credential file, it compares
// the stat data of the index entry with the file, as git does, and hashes the file in Go only
// when the stat data differs. It never runs git add, because git add runs the filter drivers
// of the repository. The check runs the system git with the clean child env, for at most 5 s.
//
// The check cannot know which files an earlier part of the command makes. So a broad add after
// a part that can write a file is unchecked, and a named path that does not exist is unchecked
// after a part that can make a folder.
func StagedCredentials(ctx context.Context, p security.Parsed, workdir string) (paths []string, unchecked bool) {
	cwd, known := workdir, true
	ctx, cancel := context.WithTimeout(ctx, stageTimeout)
	defer cancel()
	s := &stageCheck{ctx: ctx, envSet: gitEnvAssignRe.MatchString(p.View)}
	cdChecked, cdMoves := false, false
	found := map[string]bool{}
	for i, c := range p.Cmds {
		if len(c.Argv) == 0 {
			continue
		}
		// The shell opens the file of a redirect before the program starts.
		if redirectWrites(c.Redirs) {
			s.writes = true
		}
		switch path.Base(c.Argv[0]) {
		case "cd", "pushd", "popd":
			if !cdChecked {
				cdMoves, cdChecked = !cdOutsideShell(p.View), true
			}
			if c.Depth > 0 || !cdMoves || path.Base(c.Argv[0]) == "popd" {
				// The cd runs in another shell, or at a time that the check cannot tell.
				known = false
			} else {
				cwd, known = moveDir(cwd, known, c.Argv[1:])
			}
			continue
		}
		if g, ok := parseGitAdd(c.Argv); ok {
			names, bad := s.addPart(p.Cmds, i, g, cwd, known)
			if bad {
				unchecked = true
			}
			for _, name := range names {
				found[name] = true
			}
		} else if setsGitEnv(c) {
			s.envSet = true
		}
		if partWrites(p, i) {
			s.writes, s.mkdirs = true, true
		}
	}
	if unchecked {
		return nil, true
	}
	for name := range found {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	if len(paths) > maxStagedPaths {
		paths = paths[:maxStagedPaths]
	}
	return paths, false
}

// addPart checks the git add part cmds[i]. It returns the credential files that the part
// would stage, and true when the check cannot tell.
func (s *stageCheck) addPart(cmds []security.SimpleCmd, i int, g gitAdd, cwd string, known bool) ([]string, bool) {
	c := cmds[i]
	// An assignment or a variable of the command can move the repository, the work tree, the
	// index, or the config. A wrapper can run git as another user, on another host, or with
	// more paths.
	if g.unknown || c.Assigned() || s.envSet || len(c.Via) > 0 || xargsWrapped(cmds, i) {
		return nil, true
	}
	dir, dirKnown := cwd, known
	for _, d := range g.dirs {
		if filepath.IsAbs(d) {
			dirKnown = true
		}
		dir = joinDir(dir, d)
	}
	// In an unknown folder, each path can be a folder that holds a credential file.
	if !dirKnown {
		return nil, true
	}
	a := parseAddArgs(g.rest)
	if a.unknown {
		return nil, true
	}
	broad, missing := a.broad(dir)
	if missing && s.mkdirs {
		// An earlier part can make the path, for example a folder with a copy of a
		// credential file. With no such part, git add of a missing path fails or stages a
		// removal.
		return nil, true
	}
	if !broad {
		return nil, false
	}
	if s.writes || g.config {
		// An earlier part can write a credential file, and the config of the command can
		// change the ignore rules.
		return nil, true
	}
	specs, ok := a.pathspecs(dir)
	if !ok {
		return nil, true
	}
	if len(specs) == 0 {
		return nil, false
	}
	if !s.gitTried {
		s.git, s.gitOK = systemGit()
		s.gitTried = true
	}
	if !s.gitOK {
		return nil, false
	}
	names, err := stagedFiles(s.ctx, s.git, dir, g, a, specs)
	if errors.Is(err, errNotRepo) {
		return nil, false
	}
	if err != nil {
		return nil, true
	}
	return names, false
}

// errNotRepo tells that the folder of a git add part is not in a git repository.
var errNotRepo = errors.New("not a git repository")

// stagedFiles returns the credential files that the add part would stage, as paths from the
// top of the work tree.
func stagedFiles(ctx context.Context, git, dir string, g gitAdd, a addArgs, specs []string) ([]string, error) {
	run := func(args ...string) ([]byte, error) {
		out, err := gitRun(ctx, git, dir, slices.Concat(safeGitArgs, g.global, args))
		if err != nil {
			if strings.Contains(string(out), "not a git repository") {
				return nil, errNotRepo
			}
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	out, err := run("rev-parse", "--show-toplevel", "--git-path", "index")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	top := strings.TrimSpace(lines[0])
	if top == "" {
		return nil, errors.New("git rev-parse: no work tree")
	}
	// The index time tells a racily clean entry. With no index time, the check hashes each
	// tracked credential file.
	var indexTime time.Time
	if len(lines) > 1 && lines[1] != "" {
		if st, err := os.Stat(joinDir(dir, lines[1])); err == nil {
			indexTime = st.ModTime()
		}
	}
	var names []string
	if !a.update {
		args := []string{"ls-files", "-z", "--others", "--full-name"}
		if !a.force {
			args = append(args, "--exclude-standard")
		}
		out, err := run(slices.Concat(args, []string{"--"}, specs)...)
		if err != nil {
			return nil, err
		}
		for name := range strings.SplitSeq(string(out), "\x00") {
			if name != "" && security.IsCredentialFile(name) {
				names = append(names, name)
			}
		}
	}
	// --debug adds the stat data of each entry. git reads it from the index and runs no filter.
	out, err = run(slices.Concat([]string{"ls-files", "-z", "--stage", "--debug", "--full-name", "--"}, specs)...)
	if err != nil {
		return nil, err
	}
	for _, e := range parseStageDebug(string(out)) {
		if e.changed(ctx, top, indexTime) {
			names = append(names, e.name)
		}
	}
	return names, nil
}

// indexEntry is one entry of git ls-files --stage --debug.
type indexEntry struct {
	mode, object, stage, name string
	// stat holds the stat data of the entry: "ctime" and "mtime" as {seconds, nanoseconds},
	// and "dev", "ino", "uid", "gid", and "size" as {value, 0}.
	stat map[string][2]uint64
}

// parseStageDebug reads the output of git ls-files -z --stage --debug. Each entry is
// "<mode> <object> <stage>\t<path>" and a NUL. The stat lines of the entry follow the NUL,
// and each stat line starts with two spaces.
func parseStageDebug(out string) []indexEntry {
	var entries []indexEntry
	for i, chunk := range strings.Split(out, "\x00") {
		for i > 0 && len(entries) > 0 && strings.HasPrefix(chunk, "  ") {
			line, rest, _ := strings.Cut(chunk, "\n")
			entries[len(entries)-1].readStat(line)
			chunk = rest
		}
		meta, name, ok := strings.Cut(chunk, "\t")
		f := strings.Fields(meta)
		if !ok || name == "" || len(f) != 3 {
			continue
		}
		entries = append(entries, indexEntry{mode: f[0], object: f[1], stage: f[2], name: name})
	}
	return entries
}

// readStat reads one stat line, for example "  dev: 2049\tino: 1234".
func (e *indexEntry) readStat(line string) {
	for item := range strings.SplitSeq(line, "\t") {
		key, val, ok := strings.Cut(strings.TrimSpace(item), ": ")
		if !ok {
			continue
		}
		secs, nsecs, hasNsec := strings.Cut(val, ":")
		a, err := strconv.ParseUint(secs, 10, 64)
		if err != nil {
			continue
		}
		var b uint64
		if hasNsec {
			if b, err = strconv.ParseUint(nsecs, 10, 64); err != nil {
				continue
			}
		}
		if e.stat == nil {
			e.stat = map[string][2]uint64{}
		}
		e.stat[key] = [2]uint64{a, b}
	}
}

// changed reports whether git add would stage a new content of the entry: the entry is a
// credential file, and the work tree has a new content for it. A file with the same stat data
// as the entry is not changed, as git does. Else the check hashes the file in Go, because git
// can run a filter driver when it compares a file with the index.
func (e indexEntry) changed(ctx context.Context, top string, indexTime time.Time) bool {
	if !security.IsCredentialFile(e.name) || e.mode == "160000" {
		return false
	}
	full := filepath.Join(top, filepath.FromSlash(e.name))
	st, err := os.Lstat(full)
	if err != nil {
		// git add stages no content for a removed file.
		return false
	}
	symlink := st.Mode()&os.ModeSymlink != 0
	if !symlink && !st.Mode().IsRegular() {
		// git does not stage a folder, a FIFO, a socket, or a device as a file.
		return false
	}
	if e.stage != "0" {
		// git add stages the file of a merge conflict.
		return true
	}
	if e.statClean(st, indexTime) {
		return false
	}
	var h hash.Hash
	switch len(e.object) {
	case 40:
		h = sha1.New()
	case 64:
		h = sha256.New()
	default:
		return true
	}
	if symlink {
		target, err := os.Readlink(full)
		if err != nil {
			return true
		}
		h.Write([]byte("blob " + strconv.Itoa(len(target)) + "\x00" + target))
	} else if !hashFile(ctx, h, full, st) {
		return true
	}
	return hex.EncodeToString(h.Sum(nil)) != e.object
}

// statClean reports whether the stat data of the entry matches st, as git compares it: the
// mode, the change time, the modification time, the inode, the owner, the group, and the size.
// The check compares at least the fields of git, so a file that git reads as changed is not
// clean here. An entry that git wrote in the same second as the file or later is racily clean,
// and git reads its content. So the check reads it too.
func (e indexEntry) statClean(st os.FileInfo, indexTime time.Time) bool {
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		if e.mode != "120000" {
			return false
		}
	case st.Mode()&0o100 != 0:
		if e.mode != "100755" {
			return false
		}
	default:
		if e.mode != "100644" {
			return false
		}
	}
	sys, ok := sysStat(st)
	if !ok {
		return false
	}
	mtime := st.ModTime()
	if indexTime.IsZero() || indexTime.Unix() <= mtime.Unix() {
		return false
	}
	want := map[string][2]uint64{
		"ctime": sys.ctime,
		"mtime": {uint64(mtime.Unix()), uint64(mtime.Nanosecond())},
		"ino":   {sys.ino, 0},
		"uid":   {sys.uid, 0},
		"gid":   {sys.gid, 0},
		"size":  {uint64(st.Size()), 0},
	}
	for key, w := range want {
		got, ok := e.stat[key]
		// The index keeps each value in 32 bits.
		if !ok || got[0] != w[0]&0xffffffff {
			return false
		}
		// A git with no nanosecond support writes 0 and compares the seconds only.
		if (key == "ctime" || key == "mtime") && got[1] != 0 && got[1] != w[1] {
			return false
		}
	}
	return true
}

// fileSys is the part of the system stat data that git compares and os.FileInfo does not give.
type fileSys struct {
	ctime         [2]uint64
	ino, uid, gid uint64
}

// sysStat reads the change time, the inode, the owner, and the group of st. It reads the fields
// by name, because the syscall.Stat_t of Linux and of macOS name the change time differently.
func sysStat(st os.FileInfo) (fileSys, bool) {
	var out fileSys
	v := reflect.ValueOf(st.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return out, false
	}
	num := func(v reflect.Value) (uint64, bool) {
		switch {
		case !v.IsValid():
			return 0, false
		case v.CanInt():
			return uint64(v.Int()), true
		case v.CanUint():
			return v.Uint(), true
		}
		return 0, false
	}
	ct := v.FieldByName("Ctim")
	if !ct.IsValid() {
		ct = v.FieldByName("Ctimespec")
	}
	if !ct.IsValid() || ct.Kind() != reflect.Struct {
		return out, false
	}
	var ok [5]bool
	out.ctime[0], ok[0] = num(ct.FieldByName("Sec"))
	out.ctime[1], ok[1] = num(ct.FieldByName("Nsec"))
	out.ino, ok[2] = num(v.FieldByName("Ino"))
	out.uid, ok[3] = num(v.FieldByName("Uid"))
	out.gid, ok[4] = num(v.FieldByName("Gid"))
	return out, ok == [5]bool{true, true, true, true, true}
}

// hashFile writes the git blob header and the content of the regular file at full into h. It
// returns false when the file is larger than maxHashBytes, when the file is not the file of
// st, or when the time of ctx ends. It opens the file with no wait and does not follow a
// symlink, so a FIFO that takes the place of the file cannot block the check.
func hashFile(ctx context.Context, h hash.Hash, full string, st os.FileInfo) bool {
	if st.Size() > maxHashBytes {
		return false
	}
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	fst, err := f.Stat()
	if err != nil || !fst.Mode().IsRegular() || !os.SameFile(st, fst) || fst.Size() != st.Size() {
		return false
	}
	h.Write([]byte("blob " + strconv.FormatInt(st.Size(), 10) + "\x00"))
	n, err := io.Copy(h, io.LimitReader(ctxReader{ctx, f}, st.Size()+1))
	return err == nil && n == st.Size()
}

// ctxReader stops a read when the time of ctx ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// cdOutsideShell reports whether a cd, pushd, or popd of text runs outside the shell of the
// later parts: in a subshell, a substitution, a pipeline, a background job, a coprocess, or a
// function. Such a cd does not move the folder of the later parts, or moves it at a time that
// the check cannot tell. It returns true when text cannot be parsed.
func cdOutsideShell(text string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
	if err != nil {
		return true
	}
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *syntax.Subshell, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.CoprocClause, *syntax.FuncDecl:
			found = hasCd(x)
			return false
		case *syntax.BinaryCmd:
			if x.Op == syntax.Pipe || x.Op == syntax.PipeAll {
				found = hasCd(x)
				return false
			}
		case *syntax.Stmt:
			if x.Background {
				found = hasCd(x)
				return false
			}
		}
		return true
	})
	return found
}

// hasCd reports whether a simple command under n has the word cd, pushd, or popd.
func hasCd(n syntax.Node) bool {
	found := false
	syntax.Walk(n, func(x syntax.Node) bool {
		if c, ok := x.(*syntax.CallExpr); ok {
			for _, w := range c.Args {
				switch litWord(w) {
				case "cd", "pushd", "popd":
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// litWord returns the text of a word with no expansion, after quote removal. It returns ""
// for a word with an expansion.
func litWord(w *syntax.Word) string {
	var b strings.Builder
	var add func(parts []syntax.WordPart) bool
	add = func(parts []syntax.WordPart) bool {
		for _, p := range parts {
			switch v := p.(type) {
			case *syntax.Lit:
				b.WriteString(strings.ReplaceAll(v.Value, `\`, ""))
			case *syntax.SglQuoted:
				b.WriteString(v.Value)
			case *syntax.DblQuoted:
				if !add(v.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !add(w.Parts) {
		return ""
	}
	return b.String()
}

// redirectWrites reports whether one of the redirects can write a file. A copy of a file
// descriptor and a write to /dev/null do not write a file. An unknown operator counts as a
// write.
func redirectWrites(redirs []string) bool {
	for i := 0; i+1 < len(redirs); i += 2 {
		op, target := strings.TrimLeft(redirs[i], "0123456789"), redirs[i+1]
		switch op {
		case "<", "<<", "<<<", "<<-", "<&":
			continue
		case ">&":
			if target == "-" || (target != "" && strings.Trim(target, "0123456789") == "") {
				continue
			}
		case ">", ">>", "&>", "&>>", "<>", ">|":
		default:
			return true
		}
		switch target {
		case "/dev/null", "/dev/stdout", "/dev/stderr":
			continue
		}
		return true
	}
	return len(redirs)%2 != 0
}

// readOnlyPrograms are the programs that write no file and make no folder with no redirect.
var readOnlyPrograms = []string{"cd", "pushd", "popd", "true", "false", ":", "ls", "cat", "echo",
	"printf", "pwd", "test", "["}

// readOnlyGitCommands are the git subcommands that write no file in the work tree.
var readOnlyGitCommands = []string{"status", "diff", "log", "show", "ls-files", "rev-parse"}

// readOnlyGitFlags are the git global options with no value that start no other program.
var readOnlyGitFlags = []string{"--no-pager", "-P", "--no-optional-locks", "--literal-pathspecs",
	"--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-replace-objects"}

// partWrites reports whether part i of p can write a file or make a folder with its program.
// A wrapper that runs the next part is not such a part: the next part tells. A program in a
// folder other than /bin and /usr/bin can be any program.
func partWrites(p security.Parsed, i int) bool {
	c := p.Cmds[i]
	if wrapsNext(p.Cmds, i) {
		return false
	}
	prog := c.Argv[0]
	if d := path.Dir(prog); strings.Contains(prog, "/") && d != "/bin" && d != "/usr/bin" {
		return true
	}
	if p.LookupChanged() {
		return true
	}
	if path.Base(prog) == "git" {
		return !readOnlyGit(c.Argv)
	}
	return !slices.Contains(readOnlyPrograms, path.Base(prog))
}

// readOnlyGit reports whether argv is a git command that writes no file in the work tree.
// An option that sets the config or starts another program makes it not read-only.
func readOnlyGit(argv []string) bool {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case slices.Contains(readOnlyGitFlags, a), strings.HasPrefix(a, "--git-dir="),
			strings.HasPrefix(a, "--work-tree="), strings.HasPrefix(a, "--namespace="):
		case strings.HasPrefix(a, "-"):
			return false
		default:
			if !slices.Contains(readOnlyGitCommands, a) {
				return false
			}
			for _, w := range argv[i+1:] {
				if w == "--" {
					break
				}
				// --output writes the diff to a file.
				if w == "--output" || strings.HasPrefix(w, "--output=") {
					return false
				}
			}
			return true
		}
	}
	return false
}

// wrapsNext reports whether part i is a wrapper that runs part i+1: the next part is inside
// a new shell of the wrapper, or its words are the last words of the wrapper.
func wrapsNext(cmds []security.SimpleCmd, i int) bool {
	if _, ok := security.LookupWrapper(path.Base(cmds[i].Argv[0])); !ok || i+1 >= len(cmds) {
		return false
	}
	next := cmds[i+1]
	return next.Depth > cmds[i].Depth || endsWith(cmds[i].Argv, next.Argv)
}

// xargsWrapped reports whether an xargs part runs part i. xargs adds paths from its input.
func xargsWrapped(cmds []security.SimpleCmd, i int) bool {
	for j := range i {
		if path.Base(cmds[j].Argv[0]) == "xargs" && endsWith(cmds[j].Argv, cmds[i].Argv) {
			return true
		}
	}
	return false
}

// endsWith reports whether the last words of long are short, and long has more words.
func endsWith(long, short []string) bool {
	return len(short) > 0 && len(long) > len(short) && slices.Equal(long[len(long)-len(short):], short)
}

// setsGitEnv reports whether c can set or remove a variable of gitEnvNameRe, as in
// "export GIT_DIR=x", "unset HOME", or "read GIT_DIR".
func setsGitEnv(c security.SimpleCmd) bool {
	switch path.Base(c.Argv[0]) {
	case "echo", "printf", "cat", "ls", "test", "[", "cd", "pushd", "popd":
		return false
	}
	return slices.ContainsFunc(c.Argv[1:], gitEnvNameRe.MatchString)
}

// moveDir returns the folder after a literal cd or pushd. It returns known false when the
// folder depends on the shell state.
func moveDir(cwd string, known bool, args []string) (string, bool) {
	dir := ""
	for _, a := range args {
		if a == "-" {
			return cwd, false
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		dir = a
		break
	}
	if dir == "" || strings.ContainsAny(dir, "$`~") || !known {
		return cwd, false
	}
	return joinDir(cwd, dir), true
}

// joinDir joins a folder of a command to the folder before it.
func joinDir(base, dir string) string {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(base, dir)
}

// gitAdd is a git add or git stage part with its global options split from its arguments.
type gitAdd struct {
	dirs    []string // the -C values, in order
	global  []string // the --git-dir, --work-tree, and --icase-pathspecs options
	rest    []string // the arguments after add or stage
	config  bool     // the part sets config with -c or --config-env
	unknown bool     // a value depends on the shell
}

// parseGitAdd returns the parts of argv when it is a git add or git stage command.
func parseGitAdd(argv []string) (gitAdd, bool) {
	var g gitAdd
	if path.Base(argv[0]) != "git" {
		return g, false
	}
	i := 1
	for i < len(argv) {
		a := argv[i]
		switch {
		case security.IsGitValueOption(a):
			if i+1 >= len(argv) {
				return g, false
			}
			switch a {
			case "-C":
				g.dirs = append(g.dirs, argv[i+1])
			case "-c", "--config-env":
				g.config = true
			case "--git-dir", "--work-tree":
				g.global = append(g.global, a+"="+argv[i+1])
			}
			i += 2
		case strings.HasPrefix(a, "--config-env="):
			g.config = true
			i++
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree=") || a == "--icase-pathspecs":
			g.global = append(g.global, a)
			i++
		case strings.HasPrefix(a, "-"):
			i++
		default:
			if a != "add" && a != "stage" {
				return g, false
			}
			g.rest = argv[i+1:]
			for _, v := range slices.Concat(g.dirs, g.global) {
				if strings.ContainsAny(v, "$`~") {
					g.unknown = true
				}
			}
			return g, true
		}
	}
	return g, false
}

// addArgs are the arguments of a git add part that select the files.
type addArgs struct {
	all, update, force bool
	fromFile           string // the --pathspec-from-file value
	hasFromFile        bool
	fileNul            bool     // --pathspec-file-nul
	paths              []string // the pathspecs of the command text
	unknown            bool     // an option that the check cannot read
}

// parseAddArgs reads the arguments after git add.
func parseAddArgs(rest []string) addArgs {
	var a addArgs
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if w == "--" {
			a.paths = append(a.paths, rest[i+1:]...)
			break
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			a.paths = append(a.paths, w)
			continue
		}
		if strings.ContainsAny(w, "$`") {
			// The shell expands the option at run time.
			a.unknown = true
			continue
		}
		if !strings.HasPrefix(w, "--") {
			a.all = a.all || strings.Contains(w, "A")
			a.update = a.update || strings.Contains(w, "u")
			a.force = a.force || strings.Contains(w, "f")
			continue
		}
		body, val, hasVal := strings.Cut(w[2:], "=")
		name, neg, ok := security.GitAddLongOption(body)
		if !ok {
			a.unknown = true
			continue
		}
		switch name {
		case "all":
			a.all = !neg
		case "ignore-removal":
			a.all = neg
		case "update", "renormalize":
			a.update = !neg
		case "force":
			a.force = !neg
		case "pathspec-file-nul":
			a.fileNul = !neg
		case "chmod", "pathspec-from-file":
			if !hasVal {
				if i+1 >= len(rest) {
					a.unknown = true
					continue
				}
				i++
				val = rest[i]
			}
			if name == "pathspec-from-file" {
				a.fromFile, a.hasFromFile = val, !neg
			}
		}
	}
	if a.all {
		// git refuses -A with -u. The check takes the larger form.
		a.update = false
	}
	if slices.ContainsFunc(a.paths, func(p string) bool { return strings.ContainsAny(p, "$`") }) {
		// The shell expands the path at run time.
		a.unknown = true
	}
	return a
}

// broad reports whether the command stages files that its text does not name. It looks at
// the disk in dir for a path that is a folder. missing is true when a path does not exist.
func (a addArgs) broad(dir string) (broad, missing bool) {
	broad = a.all || a.update || a.hasFromFile
	for _, p := range a.paths {
		if p == "." || strings.HasPrefix(p, ":") || strings.ContainsAny(p, "*?[") {
			broad = true
			continue
		}
		st, err := os.Stat(joinDir(dir, p))
		if err != nil {
			missing = true
			continue
		}
		if st.IsDir() {
			broad = true
		}
	}
	return broad, missing
}

// pathspecs returns the pathspecs that the command stages, with the lines of the
// --pathspec-from-file file. It returns ok false when the file cannot be read.
func (a addArgs) pathspecs(dir string) ([]string, bool) {
	specs := slices.Clone(a.paths)
	if a.hasFromFile {
		if a.fromFile == "-" || a.fromFile == "" {
			return nil, false
		}
		data, err := os.ReadFile(joinDir(dir, a.fromFile))
		if err != nil {
			return nil, false
		}
		sep := "\n"
		if a.fileNul {
			sep = "\x00"
		}
		for line := range strings.SplitSeq(string(data), sep) {
			if !a.fileNul {
				line = strings.TrimSuffix(line, "\r")
				if strings.HasPrefix(line, `"`) {
					u, err := strconv.Unquote(line)
					if err != nil {
						return nil, false
					}
					line = u
				}
			}
			if line != "" {
				specs = append(specs, line)
			}
		}
	}
	if len(specs) == 0 {
		if a.all || a.update {
			// With no pathspec, -A and -u stage the full work tree.
			return []string{":/"}, true
		}
		return nil, true
	}
	if !slices.ContainsFunc(specs, func(s string) bool { return !excludeSpec(s) }) {
		// git add reads a list of only exclude pathspecs as the full work tree less those
		// paths. git ls-files reads it as the current folder less those paths.
		specs = append(specs, ":/")
	}
	return specs, true
}

// excludeSpec reports whether s is an exclude pathspec, for example ":!x" or ":(exclude)x".
func excludeSpec(s string) bool {
	if strings.HasPrefix(s, ":!") || strings.HasPrefix(s, ":^") {
		return true
	}
	magic, ok := strings.CutPrefix(s, ":(")
	if !ok {
		return false
	}
	magic, _, _ = strings.Cut(magic, ")")
	return slices.Contains(strings.Split(magic, ","), "exclude")
}
