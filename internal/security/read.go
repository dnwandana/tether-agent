package security

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// The limits of ReadCommand.
const (
	MaxCommandBytes = 32768 // the longest command text, in bytes
	MaxNesting      = 40    // the deepest nesting of substitutions and subshells
	MaxParseDepth   = 4     // the most new parses on one path
	MaxWrapperChain = 16    // the most wrappers on one command
	MaxBraceWords   = 256   // the most words that brace expansion makes in one simple command
)

// Parsed is the result of ReadCommand.
//
// Read is false when the reader could not read the command to the end. Why then gives the
// reason. Cmds holds each simple command, outer and inner, in source order. RuntimeText is
// true when the command holds text that the shell builds at run time, such as a parameter
// expansion or a command substitution. View is the text that the rule matching reads.
type Parsed struct {
	Read        bool
	Why         string
	Cmds        []SimpleCmd
	RuntimeText bool
	View        string

	// lookupChanged is true when the command changes how the shell finds a program: it sets
	// PATH or BASH_CMDS, or runs hash or enable.
	lookupChanged bool
}

// SimpleCmd is one simple command after quote removal.
//
// Argv holds the program word and its arguments. Redirs holds each redirect operator and its
// target word. Via and Depth tell which wrappers the command sits behind. Data marks a
// command that only prints or stores text.
type SimpleCmd struct {
	Argv   []string
	Redirs []string
	Via    []string
	Depth  int
	Data   bool
	// assigned is true when a variable assignment comes before the program word.
	assigned bool
}

const (
	whyTooLong  = "the command is longer than 32 KB"
	whyNesting  = "the command nests more than 40 levels"
	whyRuntime  = "the program name is built at run time"
	whyEmpty    = "the program name is empty"
	whySyntax   = "the command has a syntax error: "
	whyInternal = "internal error"
	whyBraces   = "the command expands to more than 256 words"
	whyBraceSet = "the command has more than 256 braces in one word"
)

// reader holds the state of one ReadCommand call.
type reader struct {
	src string // the text of the current parse; word offsets point into it
	out *Parsed

	spans []span     // the comments, which the view removes
	data  []dataSpan // the text of the data commands, which the view removes
	// noData is true when a command can send the text of a data command to a shell through
	// a file descriptor: an exec with a redirect, a coproc, or a redirect to another file
	// descriptor.
	noData bool
	// shadow holds the names that a function or an alias defines. Such a name does not run
	// the data program.
	shadow map[string]bool
}

// dataSpan is a part of the raw text of the data command Cmds[cmd].
type dataSpan struct {
	cmd int
	span
}

// walkCtx is the position of a node in the walk.
type walkCtx struct {
	nest   int      // the CmdSubst, ProcSubst, and Subshell nodes above the node
	redirs []string // the redirects of an enclosing compound command
	depth  int      // the new parses on the path: 0 for an outer command
	via    []string // the privilege and host wrappers on the path
	chain  int      // the wrappers on the path

	piped   bool               // a pipe reads the output of the node
	inSubst bool               // the node is inside a CmdSubst or a ProcSubst
	rds     []*syntax.Redirect // the redirects of the statement and of the enclosing statements
	own     []*syntax.Redirect // the redirects of the statement of the node
}

// ReadCommand reads a shell command as bash does. It finds each simple command, outer and
// inner, and removes the quotes of each word. It does not expand a parameter and does not run
// anything.
func ReadCommand(text string) (res Parsed) {
	res = Parsed{View: text}
	if len(text) > MaxCommandBytes {
		res.Why = whyTooLong
		return res
	}
	defer func() {
		if recover() != nil {
			res = Parsed{Why: whyInternal, View: text}
		}
	}()
	r := &reader{out: &res, shadow: map[string]bool{}}
	r.parse(text, walkCtx{})
	res.Read = res.Why == ""
	if res.Read {
		r.endData()
		res.View = buildView(text, r.spans)
	} else {
		res.View = text
		for i := range res.Cmds {
			res.Cmds[i].Data = false
		}
	}
	return res
}

// parse reads text with a new parser and walks the result.
func (r *reader) parse(text string, wc walkCtx) {
	p := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(true))
	f, err := p.Parse(strings.NewReader(text), "")
	if err != nil {
		r.fail(whySyntax + err.Error())
		return
	}
	if wc.depth == 0 {
		syntax.Walk(f, func(n syntax.Node) bool {
			if c, ok := n.(*syntax.Comment); ok {
				r.spans = append(r.spans, span{int(c.Hash.Offset()), int(c.End().Offset())})
			}
			return true
		})
	}
	saved := r.src
	r.src = text
	r.stmts(f.Stmts, wc)
	r.src = saved
}

// endData removes the Data mark of each command that can feed a shell, and adds the text of
// each other data command to the spans. A later statement can change an earlier one, so this
// step comes after the walk.
func (r *reader) endData() {
	for i, c := range r.out.Cmds {
		if c.Data && (r.noData || r.shadow[c.Argv[0]]) {
			r.out.Cmds[i].Data = false
		}
	}
	for _, d := range r.data {
		if r.out.Cmds[d.cmd].Data {
			r.spans = append(r.spans, d.span)
		}
	}
}

// fail records why the command cannot be read. The first reason wins.
func (r *reader) fail(why string) {
	if r.out.Why == "" {
		r.out.Why = why
	}
}

func (r *reader) stmts(list []*syntax.Stmt, wc walkCtx) {
	for _, s := range list {
		r.stmt(s, wc)
	}
}

func (r *reader) stmt(s *syntax.Stmt, wc walkCtx) {
	if s == nil {
		return
	}
	all := append([]string(nil), wc.redirs...)
	for _, rd := range s.Redirs {
		all = append(all, r.redirStrings(rd)...)
		if r.fdRedirect(rd) {
			r.noData = true
		}
	}
	if s.Cmd != nil {
		sc := wc
		sc.own = s.Redirs
		sc.rds = append(slices.Clone(wc.rds), s.Redirs...)
		r.command(s.Cmd, all, sc)
	}
	// The redirect words and the heredoc bodies come after the command, so a command that
	// reads a heredoc comes before the commands in the body.
	for _, rd := range s.Redirs {
		r.subst(rd, wc)
	}
}

// fdRedirect reports whether a redirect can send output to a file descriptor other than 1
// and 2. Such a descriptor can be the input of a shell.
func (r *reader) fdRedirect(rd *syntax.Redirect) bool {
	if rd.Word == nil {
		return false
	}
	w, lit := r.word(rd.Word)
	if rd.Op == syntax.DplOut || rd.Op == syntax.DplIn {
		return !lit || (w != "1" && w != "2" && w != "-" && strings.Trim(w, "0123456789") == "")
	}
	switch {
	case w == "/dev/fd/1" || w == "/dev/fd/2":
		return false
	case strings.HasPrefix(w, "/dev/fd/"), strings.HasPrefix(w, "/proc/"):
		return true
	}
	return false
}

// redirStrings returns the operator (with its fd number) and the target word of a redirect.
// The clobber operator ">|" writes the file as ">" does, so the rules read it as ">".
func (r *reader) redirStrings(rd *syntax.Redirect) []string {
	op := rd.Op.String()
	if rd.Op == syntax.RdrClob {
		op = ">"
	}
	if rd.N != nil {
		op = rd.N.Value + op
	}
	out := []string{op}
	if rd.Word != nil {
		w, _ := r.word(rd.Word)
		out = append(out, w)
	}
	return out
}

func (r *reader) command(cmd syntax.Command, redirs []string, wc walkCtx) {
	inner := wc
	inner.redirs = redirs
	inner.own = nil
	switch c := cmd.(type) {
	case *syntax.CallExpr:
		for _, a := range c.Assigns {
			r.assign(a)
			r.subst(a, wc)
		}
		if len(c.Args) == 0 {
			return
		}
		argv, lit, ok := r.argWords(c.Args)
		if !ok {
			return
		}
		if len(argv) == 0 {
			// Brace expansion removed all words, so the command runs nothing.
			for _, w := range c.Args {
				r.subst(w, wc)
			}
			return
		}
		sc := SimpleCmd{Argv: argv, Redirs: append([]string(nil), redirs...), assigned: len(c.Assigns) > 0}
		procArg := slices.ContainsFunc(c.Args, func(w *syntax.Word) bool { return hasSubst(w, true) })
		sc.Data = !procArg && isDataCmd(argv, wc)
		if sc.Data && wc.depth == 0 {
			r.dataSpans(c, argv[0], len(r.out.Cmds), wc)
		}
		r.add(sc, lit, wc)
		for _, w := range c.Args {
			r.subst(w, wc)
		}
	case *syntax.DeclClause:
		argv := []string{c.Variant.Value}
		lit := []bool{true}
		for _, a := range c.Args {
			r.assign(a)
			text, l := r.assignText(a)
			argv = append(argv, text)
			lit = append(lit, l)
		}
		r.add(SimpleCmd{Argv: argv, Redirs: append([]string(nil), redirs...)}, lit, wc)
		for _, a := range c.Args {
			r.subst(a, wc)
		}
	case *syntax.TimeClause:
		r.stmt(c.Stmt, inner)
	case *syntax.CoprocClause:
		// The shell reads the output of the coproc, and a later redirect can write to its
		// input.
		r.noData = true
		inner.piped = true
		r.stmt(c.Stmt, inner)
	case *syntax.Block:
		r.stmts(c.Stmts, inner)
	case *syntax.Subshell:
		if wc.nest+1 > MaxNesting {
			r.fail(whyNesting)
			return
		}
		inner.nest++
		r.stmts(c.Stmts, inner)
	case *syntax.IfClause:
		for ic := c; ic != nil; ic = ic.Else {
			r.stmts(ic.Cond, inner)
			r.stmts(ic.Then, inner)
		}
	case *syntax.WhileClause:
		r.stmts(c.Cond, inner)
		r.stmts(c.Do, inner)
	case *syntax.ForClause:
		switch l := c.Loop.(type) {
		case *syntax.WordIter:
			for _, w := range l.Items {
				r.subst(w, wc)
			}
		case *syntax.CStyleLoop:
			r.subst(l, wc)
		}
		r.stmts(c.Do, inner)
	case *syntax.CaseClause:
		r.subst(c.Word, wc)
		for _, it := range c.Items {
			for _, p := range it.Patterns {
				r.subst(p, wc)
			}
			r.stmts(it.Stmts, inner)
		}
	case *syntax.FuncDecl:
		if c.Name != nil {
			r.shadow[c.Name.Value] = true
		}
		for _, n := range c.Names {
			r.shadow[n.Value] = true
		}
		r.stmt(c.Body, inner)
	case *syntax.BinaryCmd:
		x := inner
		if c.Op == syntax.Pipe || c.Op == syntax.PipeAll {
			x.piped = true
		}
		r.stmt(c.X, x)
		r.stmt(c.Y, inner)
	case *syntax.TestClause, *syntax.ArithmCmd, *syntax.LetClause:
		r.subst(c, wc)
	case *syntax.TestDecl:
		r.stmt(c.Body, inner)
	}
}

// assignText returns the Name=value text of a declaration argument.
func (r *reader) assignText(a *syntax.Assign) (string, bool) {
	if a.Naked {
		if a.Value != nil {
			return r.word(a.Value)
		}
		return a.Name.Value, true
	}
	if a.Index != nil || a.Array != nil || a.Name == nil {
		return r.slice(a), false
	}
	op := "="
	if a.Append {
		op = "+="
	}
	if a.Value == nil {
		return a.Name.Value + op, true
	}
	v, lit := r.word(a.Value)
	return a.Name.Value + op + v, lit
}

// assign checks the name of a variable assignment.
func (r *reader) assign(a *syntax.Assign) {
	if a.Name != nil {
		r.setsVar(a.Name.Value)
	}
}

// setsVar records the effect of a command that sets the variable name.
func (r *reader) setsVar(name string) {
	switch name {
	case "PATH", "BASH_CMDS":
		r.out.lookupChanged = true
	case "BASH_ALIASES":
		r.noData = true
	}
}

// add appends a simple command and checks its program word.
func (r *reader) add(c SimpleCmd, lit []bool, wc walkCtx) {
	c.Depth = wc.depth
	c.Via = append([]string(nil), wc.via...)
	r.out.Cmds = append(r.out.Cmds, c)
	switch filepath.Base(c.Argv[0]) {
	case "exec":
		// An exec with a redirect changes the output of all later commands.
		if len(wc.rds) > 0 {
			r.noData = true
		}
	case "hash", "enable":
		r.out.lookupChanged = true
	case "alias":
		for _, a := range c.Argv[1:] {
			if name, _, ok := strings.Cut(a, "="); ok {
				r.shadow[name] = true
			}
		}
	}
	// A word can name a variable that the command sets, as in "read PATH".
	for _, a := range c.Argv[1:] {
		if i := strings.IndexAny(a, "=+["); i >= 0 {
			a = a[:i]
		}
		r.setsVar(a)
	}
	switch {
	case !lit[0]:
		r.fail(whyRuntime)
	case c.Argv[0] == "":
		r.fail(whyEmpty)
	}
	if r.out.Why == "" {
		r.unwrap(c, lit, wc)
	}
}

// subst walks a node for expansions and substitutions. It sets RuntimeText and reads the
// commands inside a substitution.
func (r *reader) subst(n syntax.Node, wc walkCtx) {
	if n == nil {
		return
	}
	syntax.Walk(n, func(x syntax.Node) bool {
		switch s := x.(type) {
		case *syntax.ParamExp, *syntax.ArithmExp:
			r.out.RuntimeText = true
		case *syntax.CmdSubst:
			r.out.RuntimeText = true
			r.nested(s.Stmts, wc)
			return false
		case *syntax.ProcSubst:
			r.out.RuntimeText = true
			r.nested(s.Stmts, wc)
			return false
		}
		return true
	})
}

// nested walks the statements of a substitution one level deeper.
func (r *reader) nested(list []*syntax.Stmt, wc walkCtx) {
	if wc.nest+1 > MaxNesting {
		r.fail(whyNesting)
		return
	}
	// The redirects of an enclosing compound command do not apply inside a substitution.
	wc.nest++
	wc.redirs = nil
	wc.rds = nil
	wc.own = nil
	wc.inSubst = true
	r.stmts(list, wc)
}

// slice returns the source text of a node.
func (r *reader) slice(n syntax.Node) string {
	a, b := int(n.Pos().Offset()), int(n.End().Offset())
	if a < 0 || b > len(r.src) || a > b {
		return ""
	}
	return r.src[a:b]
}

// word returns the value of a word after quote removal. literal is false when the word has
// an expansion or a substitution. Such a part keeps its source text.
func (r *reader) word(w *syntax.Word) (text string, literal bool) {
	var b strings.Builder
	literal = true
	for _, p := range w.Parts {
		if !r.part(&b, p, false) {
			literal = false
		}
	}
	return b.String(), literal
}

// part writes the value of one word part. It returns false for a part that is not literal.
func (r *reader) part(b *strings.Builder, p syntax.WordPart, inDouble bool) bool {
	switch v := p.(type) {
	case *syntax.Lit:
		if inDouble {
			b.WriteString(unquoteDouble(v.Value))
		} else {
			b.WriteString(unquoteBare(v.Value))
		}
		return true
	case *syntax.SglQuoted:
		if v.Dollar {
			b.WriteString(decodeANSIC(v.Value))
		} else {
			b.WriteString(v.Value)
		}
		return true
	case *syntax.DblQuoted:
		ok := true
		for _, q := range v.Parts {
			if !r.part(b, q, true) {
				ok = false
			}
		}
		return ok
	default:
		b.WriteString(r.slice(p))
		return false
	}
}

// unquoteBare decodes the backslash escapes of text outside quotes.
func unquoteBare(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		if s[i] != '\n' {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// unquoteDouble decodes the backslash escapes of text inside double quotes.
func unquoteDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case '$', '`', '"', '\\':
				i++
			case '\n':
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// decodeANSIC decodes the text of a $'...' string as bash does.
func decodeANSIC(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch c := s[i]; c {
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'e', 'E':
			b.WriteByte(27)
		case 'f':
			b.WriteByte(12)
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte(11)
		case '\\', '\'', '"', '?':
			b.WriteByte(c)
		case '0', '1', '2', '3', '4', '5', '6', '7':
			n := 1
			for n < 3 && i+n < len(s) && s[i+n] >= '0' && s[i+n] <= '7' {
				n++
			}
			v, _ := strconv.ParseUint(s[i:i+n], 8, 16)
			i += n - 1
			if byte(v) == 0 {
				return b.String()
			}
			b.WriteByte(byte(v))
		case 'x', 'u', 'U':
			max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
			n := 0
			for n < max && i+1+n < len(s) && isHex(s[i+1+n]) {
				n++
			}
			if n == 0 {
				b.WriteByte('\\')
				b.WriteByte(c)
				break
			}
			v, _ := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
			i += n
			switch {
			case c == 'x' && v == 0:
				return b.String()
			case c == 'x':
				b.WriteByte(byte(v))
			case v == 0:
				return b.String()
			case utf8.ValidRune(rune(v)):
				b.WriteRune(rune(v))
			default:
				b.WriteRune(utf8.RuneError)
			}
		case 'c':
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i] & 0x1f)
			} else {
				b.WriteString(`\c`)
			}
		default:
			b.WriteByte('\\')
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// isDataCmd reports whether a command with this argv only prints or stores text. The output
// must not go to a pipe, a substitution, or a process substitution.
func isDataCmd(argv []string, wc walkCtx) bool {
	if len(argv) == 0 || !dataProgram(argv[0]) || wc.piped || wc.inSubst {
		return false
	}
	// printf -v sets a variable that a later word can run.
	if argv[0] == "printf" && len(argv) > 1 && strings.HasPrefix(argv[1], "-v") {
		return false
	}
	for _, rd := range wc.rds {
		if rd.Word != nil && hasSubst(rd.Word, true) {
			return false
		}
	}
	return true
}

// hasSubst reports whether n holds a command substitution or a process substitution. When
// procOnly is true, only a process substitution counts.
func hasSubst(n syntax.Node, procOnly bool) bool {
	found := false
	syntax.Walk(n, func(x syntax.Node) bool {
		switch x.(type) {
		case *syntax.ProcSubst:
			found = true
		case *syntax.CmdSubst:
			found = found || !procOnly
		}
		return !found
	})
	return found
}

// dataSpans adds the parts of the data command Cmds[cmd] that the view removes.
func (r *reader) dataSpans(c *syntax.CallExpr, prog string, cmd int, wc walkCtx) {
	switch prog {
	case "echo", "printf":
		for _, w := range c.Args[1:] {
			if !hasSubst(w, false) {
				r.data = append(r.data, dataSpan{cmd, span{int(w.Pos().Offset()), int(w.End().Offset())}})
			}
		}
	default:
		for _, rd := range wc.own {
			if rd.Hdoc != nil && !hasSubst(rd.Hdoc, false) {
				r.heredocSpan(rd.Hdoc, cmd)
			}
		}
	}
}

// heredocSpan adds the body of a heredoc to the data spans. The word of a heredoc ends with
// the line of the delimiter, and that line stays in the view.
func (r *reader) heredocSpan(w *syntax.Word, cmd int) {
	a, b := int(w.Pos().Offset()), int(w.End().Offset())
	if a < 0 || b > len(r.src) || a > b {
		return
	}
	body := strings.TrimRight(r.src[a:b], "\n")
	if i := strings.LastIndexByte(body, '\n'); i >= 0 {
		r.data = append(r.data, dataSpan{cmd, span{a, a + i + 1}})
	}
}

// expWord is one word of a brace expansion while the reader builds it.
type expWord struct {
	text   []byte // the value after quote removal
	pat    []byte // the unquoted text for the glob check; a quoted part gives "x"
	lit    bool   // all parts are literal
	quoted bool   // a part is quoted, so an empty value stays a word
}

// argWords returns the argv words of a simple command, with brace expansion as bash does it.
// lit is false for a word with an expansion, a substitution, or an unquoted glob, because the
// shell makes such a word at run time. ok is false when the reader failed.
func (r *reader) argWords(args []*syntax.Word) (argv []string, lit []bool, ok bool) {
	budget := MaxBraceWords
	for _, w := range args {
		// The time of SplitBraces grows as the square of the open braces in the word.
		n := 0
		for _, p := range w.Parts {
			if l, ok := p.(*syntax.Lit); ok {
				n += strings.Count(l.Value, "{")
			}
		}
		if n > MaxBraceWords {
			r.fail(whyBraceSet)
			return nil, nil, false
		}
		cp := *w
		if !syntax.SplitBraces(&cp) {
			text, l := r.word(w)
			argv = append(argv, text)
			lit = append(lit, l && !hasGlob(r.pattern(w.Parts)))
			continue
		}
		words, ok := r.expandParts(cp.Parts, budget)
		if !ok {
			r.fail(whyBraces)
			return nil, nil, false
		}
		budget -= len(words)
		for _, e := range words {
			// bash drops an empty unquoted word, as in "{a,}".
			if len(e.text) == 0 && !e.quoted {
				continue
			}
			argv = append(argv, string(e.text))
			lit = append(lit, e.lit && !hasGlob(e.pat))
		}
	}
	return argv, lit, true
}

// pattern returns the glob text of the parts of a word: the raw text of each unquoted
// literal, and "x" for each other part.
func (r *reader) pattern(parts []syntax.WordPart) []byte {
	var b []byte
	for _, p := range parts {
		if l, ok := p.(*syntax.Lit); ok {
			b = append(b, l.Value...)
		} else {
			b = append(b, 'x')
		}
	}
	return b
}

// hasGlob reports whether the glob text has an unescaped "*", "?", or a "[" with a "]" after
// it.
func hasGlob(pat []byte) bool {
	open := false
	for i := 0; i < len(pat); i++ {
		switch pat[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			open = true
		case ']':
			if open {
				return true
			}
		}
	}
	return false
}

// expandParts returns the words that brace expansion makes from the parts of a word. ok is
// false when it makes more than limit words.
func (r *reader) expandParts(parts []syntax.WordPart, limit int) ([]expWord, bool) {
	out := []expWord{{lit: true}}
	for _, p := range parts {
		br, isBrace := p.(*syntax.BraceExp)
		if !isBrace {
			var b strings.Builder
			l := r.part(&b, p, false)
			pat := []byte{'x'}
			_, quoted := p.(*syntax.SglQuoted)
			if _, dq := p.(*syntax.DblQuoted); dq {
				quoted = true
			}
			if v, ok := p.(*syntax.Lit); ok {
				pat = []byte(v.Value)
			}
			for i := range out {
				out[i].text = append(out[i].text, b.String()...)
				out[i].pat = append(out[i].pat, pat...)
				out[i].lit = out[i].lit && l
				out[i].quoted = out[i].quoted || quoted
			}
			continue
		}
		var alts []expWord
		if br.Sequence {
			seq, ok := braceSeq(br, limit)
			if !ok {
				return nil, false
			}
			for _, v := range seq {
				alts = append(alts, expWord{text: []byte(v), pat: []byte(v), lit: true})
			}
		} else {
			for _, e := range br.Elems {
				sub, ok := r.expandParts(e.Parts, limit)
				if !ok || len(alts)+len(sub) > limit {
					return nil, false
				}
				alts = append(alts, sub...)
			}
		}
		if len(out)*len(alts) > limit {
			return nil, false
		}
		next := make([]expWord, 0, len(out)*len(alts))
		for _, o := range out {
			for _, a := range alts {
				next = append(next, expWord{
					text:   append(slices.Clip(o.text), a.text...),
					pat:    append(slices.Clip(o.pat), a.pat...),
					lit:    o.lit && a.lit,
					quoted: o.quoted || a.quoted,
				})
			}
		}
		out = next
	}
	return out, true
}

// braceSeq returns the words of a sequence such as {1..5}, {a..e}, or {01..10..2}, as bash
// makes them. ok is false when the sequence has more than limit words.
func braceSeq(br *syntax.BraceExp, limit int) ([]string, bool) {
	fromLit, toLit := br.Elems[0].Lit(), br.Elems[1].Lit()
	from, err1 := strconv.ParseInt(fromLit, 10, 64)
	to, err2 := strconv.ParseInt(toLit, 10, 64)
	chars := err1 != nil || err2 != nil
	if chars {
		if fromLit == "" || toLit == "" {
			return nil, true
		}
		from, to = int64(fromLit[0]), int64(toLit[0])
	}
	width := 0
	if !chars && (leadingZero(fromLit) || leadingZero(toLit)) {
		width = max(len(fromLit), len(toLit))
	}
	var step uint64 = 1
	if len(br.Elems) > 2 {
		n, _ := strconv.ParseInt(br.Elems[2].Lit(), 10, 64)
		if n < 0 {
			n = -n
		}
		if n > 0 {
			step = uint64(n)
		}
	}
	// The distance in uint64 has no overflow for each pair of int64 values.
	dist := uint64(to) - uint64(from)
	if from > to {
		dist = uint64(from) - uint64(to)
	}
	if dist/step >= uint64(limit) {
		return nil, false
	}
	count := int(dist/step) + 1
	out := make([]string, 0, count)
	for i := range count {
		off := uint64(i) * step
		n := int64(uint64(from) + off)
		if from > to {
			n = int64(uint64(from) - off)
		}
		switch {
		case chars:
			out = append(out, string(rune(n)))
		case width > 0:
			out = append(out, fmt.Sprintf("%0*d", width, n))
		default:
			out = append(out, strconv.FormatInt(n, 10))
		}
	}
	return out, true
}

func leadingZero(s string) bool {
	s = strings.TrimPrefix(s, "-")
	return len(s) > 1 && s[0] == '0'
}
