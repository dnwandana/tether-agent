package security

import (
	"bytes"
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// This file is a port of Hermes agent/redact.py. Go RE2 has no lookaround
// and no back-references, so a small Go function replaces each Hermes regex
// that uses them. The comment on each function names the Hermes regex.

// sensitiveQueryParams is Hermes _SENSITIVE_QUERY_PARAMS. The match is exact
// and ignores case.
var sensitiveQueryParams = map[string]bool{
	"access_token":    true,
	"refresh_token":   true,
	"id_token":        true,
	"token":           true,
	"api_key":         true,
	"apikey":          true,
	"client_secret":   true,
	"password":        true,
	"auth":            true,
	"jwt":             true,
	"session":         true,
	"secret":          true,
	"key":             true,
	"code":            true,
	"signature":       true,
	"x-amz-signature": true,
}

// prefixPatterns is Hermes _PREFIX_PATTERNS: known vendor key prefixes.
var prefixPatterns = []string{
	`sk-[A-Za-z0-9_-]{10,}`,        // OpenAI, OpenRouter, Anthropic (sk-ant-)
	`ghp_[A-Za-z0-9]{10,}`,         // GitHub PAT (classic)
	`github_pat_[A-Za-z0-9_]{10,}`, // GitHub PAT (fine-grained)
	`gho_[A-Za-z0-9]{10,}`,         // GitHub OAuth access token
	`ghu_[A-Za-z0-9]{10,}`,         // GitHub user-to-server token
	`ghs_[A-Za-z0-9]{10,}`,         // GitHub server-to-server token
	`ghr_[A-Za-z0-9]{10,}`,         // GitHub refresh token
	`xox[baprs]-[A-Za-z0-9-]{10,}`, // Slack tokens
	`AIza[A-Za-z0-9_-]{30,}`,       // Google API keys
	`pplx-[A-Za-z0-9]{10,}`,        // Perplexity
	`fal_[A-Za-z0-9_-]{10,}`,       // Fal.ai
	`fc-[A-Za-z0-9]{10,}`,          // Firecrawl
	`bb_live_[A-Za-z0-9_-]{10,}`,   // BrowserBase
	`gAAAA[A-Za-z0-9_=-]{20,}`,     // Codex encrypted tokens
	`AKIA[A-Z0-9]{16}`,             // AWS access key ID
	`sk_live_[A-Za-z0-9]{10,}`,     // Stripe secret key (live)
	`sk_test_[A-Za-z0-9]{10,}`,     // Stripe secret key (test)
	`rk_live_[A-Za-z0-9]{10,}`,     // Stripe restricted key
	`SG\.[A-Za-z0-9_-]{10,}`,       // SendGrid API key
	`hf_[A-Za-z0-9]{10,}`,          // Hugging Face token
	`r8_[A-Za-z0-9]{10,}`,          // Replicate API token
	`npm_[A-Za-z0-9]{10,}`,         // npm access token
	`pypi-[A-Za-z0-9_-]{10,}`,      // PyPI API token
	`dop_v1_[A-Za-z0-9]{10,}`,      // DigitalOcean PAT
	`doo_v1_[A-Za-z0-9]{10,}`,      // DigitalOcean OAuth
	`am_[A-Za-z0-9_-]{10,}`,        // AgentMail API key
	`sk_[A-Za-z0-9_]{10,}`,         // ElevenLabs key
	`tvly-[A-Za-z0-9]{10,}`,        // Tavily search API key
	`exa_[A-Za-z0-9]{10,}`,         // Exa search API key
	`gsk_[A-Za-z0-9]{10,}`,         // Groq Cloud API key
	`syt_[A-Za-z0-9]{10,}`,         // Matrix access token
	`retaindb_[A-Za-z0-9]{10,}`,    // RetainDB API key
	`hsk-[A-Za-z0-9]{10,}`,         // Hindsight API key
	`mem0_[A-Za-z0-9]{10,}`,        // Mem0 Platform API key
	`brv_[A-Za-z0-9]{10,}`,         // ByteRover API key
	`xai-[A-Za-z0-9]{30,}`,         // xAI (Grok) API key
}

var (
	// prefixRe is Hermes _PREFIX_RE. Hermes uses a lookbehind and a
	// lookahead for [A-Za-z0-9_-]. Here, the boundary characters are part of
	// the match, and replaceTokens restarts the scan at the end of each key,
	// so that two keys with one space between them both match.
	prefixRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9_-])(` + strings.Join(prefixPatterns, "|") + `)(?:[^A-Za-z0-9_-]|$)`)

	// prefixLiterals gates prefixRe. Each pattern starts with one of them.
	prefixLiterals = literalPrefixes(prefixPatterns)

	// envAssignRe is Hermes _ENV_ASSIGN_RE without the quote back-reference.
	// redactEnvAssign handles the quotes.
	envAssignRe = regexp.MustCompile(`([A-Z0-9_]{0,50}(?:API_?KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL|AUTH)[A-Z0-9_]{0,50})\s*=\s*(\S+)`)

	// jsonFieldRe is Hermes _JSON_FIELD_RE.
	jsonFieldRe = regexp.MustCompile(`(?i)("(?:api_?key|token|secret|password|access_token|refresh_token|auth_token|bearer|secret_value|raw_secret|secret_input|key_material)")\s*:\s*"([^"]+)"`)

	// authHeaderRe is Hermes _AUTH_HEADER_RE.
	authHeaderRe = regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)(\S+)`)

	// bearerRe is not in Hermes. It catches a bearer token outside an
	// Authorization header. redactBearer also requires a digit in the token,
	// so that English text after the word "Bearer" stays.
	bearerRe = regexp.MustCompile(`(?i)\b(Bearer\s+)([A-Za-z0-9._~+/-]{16,}=*)`)

	// telegramRe is Hermes _TELEGRAM_RE. Hermes requires 8 digits.
	// Tether Agent requires 6 digits.
	telegramRe = regexp.MustCompile(`(bot)?(\d{6,}):([-A-Za-z0-9_]{30,})`)

	// privateKeyRe is Hermes _PRIVATE_KEY_RE.
	privateKeyRe = regexp.MustCompile(`-----BEGIN[A-Z ]*PRIVATE KEY-----[\s\S]*?-----END[A-Z ]*PRIVATE KEY-----`)

	// dbConnRe is Hermes _DB_CONNSTR_RE.
	dbConnRe = regexp.MustCompile(`(?i)((?:postgres(?:ql)?|mysql|mongodb(?:\+srv)?|redis|amqp)://[^:]+:)([^@]+)(@)`)

	// urlUserinfoRe is not in Hermes. It finds the password in the userinfo of an
	// http or https URL. dbConnRe covers the database schemes.
	urlUserinfoRe = regexp.MustCompile(`(?i)\b(https?://[^\s:/?#@]+:)([^\s@/?#]+)(@)`)

	// basicAuthRe is not in Hermes. It finds the value of an Authorization: Basic
	// header. The value stops at a quote, so the quote of the shell word stays.
	basicAuthRe = regexp.MustCompile(`(?i)(Authorization:\s*Basic\s+)([A-Za-z0-9+/=._~-]+)`)

	// flagSecretRe is not in Hermes. It finds the value of --password and --token,
	// in the forms "--password X" and "--password=X". A quoted value is one value.
	flagSecretRe = regexp.MustCompile(`(?i)((?:^|[\s"'])--(?:password|token)(?:=|\s+))('[^']*'|"[^"]*"|[^\s'"]\S*)`)

	// jwtRe is Hermes _JWT_RE.
	jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_=-]{4,}){0,2}`)

	// urlQueryRe is Hermes _URL_WITH_QUERY_RE.
	urlQueryRe = regexp.MustCompile(`(https?|wss?|ftp)://([^\s/?#]+)([^\s?#]*)\?([^\s#]+)(#\S*)?`)

	// httpTargetRe is Hermes _HTTP_REQUEST_TARGET_QUERY_RE.
	httpTargetRe = regexp.MustCompile(`(?i)\b((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE|CONNECT)\s+[^ \t\r\n"']*?)\?([^ \t\r\n"']+)`)

	// formBodyRe is Hermes _FORM_BODY_RE.
	formBodyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*=[^&\s]*(?:&[A-Za-z_][A-Za-z0-9_.-]*=[^&\s]*)+$`)
)

// exactOld is one text that the exact replacer finds.
type exactOld struct {
	text string // the value, or a second form of it
	name string // the .env name; "" for a NewRedactor value
	repl string // the replacement text
}

// exactSet is the exact replacer and its texts, the longest first.
type exactSet struct {
	repl *strings.Replacer
	olds []exactOld
}

// newExactSet builds an exactSet. It sorts the texts by length, longest first,
// and drops a repeated text. It returns nil when no text is left.
func newExactSet(olds []exactOld) *exactSet {
	olds = slices.Clone(olds)
	// strings.Replacer tries the old strings in argument order, so the
	// longest value goes first and wins where two values overlap.
	slices.SortStableFunc(olds, func(a, b exactOld) int {
		return cmp.Or(cmp.Compare(len(b.text), len(a.text)), strings.Compare(a.text, b.text))
	})
	olds = slices.CompactFunc(olds, func(a, b exactOld) bool { return a.text == b.text })
	if len(olds) == 0 {
		return nil
	}
	pairs := make([]string, 0, 2*len(olds))
	for _, o := range olds {
		pairs = append(pairs, o.text, o.repl)
	}
	return &exactSet{repl: strings.NewReplacer(pairs...), olds: olds}
}

// Redactor masks API keys, tokens, and credentials in text. It is safe for
// concurrent use. A nil *Redactor applies the patterns only.
type Redactor struct {
	mu    sync.Mutex               // serializes Add
	exact atomic.Pointer[exactSet] // readers load it with no lock
}

// NewRedactor returns a Redactor that also masks each value in exactValues.
// exactValues is the list of values from .env. Values shorter than 8
// characters are ignored, because they match too much ordinary text.
func NewRedactor(exactValues []string) *Redactor {
	olds := make([]exactOld, 0, len(exactValues))
	for _, v := range exactValues {
		if utf8.RuneCountInString(v) >= 8 {
			olds = append(olds, exactOld{text: v, repl: Mask(v)})
		}
	}
	r := &Redactor{}
	r.exact.Store(newExactSet(olds))
	return r
}

// Label returns the label that replaces the value of the secret name: «NAME».
func Label(name string) string {
	return "«" + name + "»"
}

// NewNamedRedactor returns a Redactor that replaces each value of 8 or more characters, and
// its JSON form, with the label «NAME». When two names have the same value, the first name in
// sorted order gives the label.
func NewNamedRedactor(values map[string]string) *Redactor {
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	slices.Sort(names)
	var olds []exactOld
	for _, n := range names {
		olds = appendNamed(olds, n, values[n])
	}
	r := &Redactor{}
	r.exact.Store(newExactSet(olds))
	return r
}

// appendNamed adds the value of name and its JSON form to olds. A value of fewer than 8
// characters is ignored.
func appendNamed(olds []exactOld, name, value string) []exactOld {
	if utf8.RuneCountInString(value) < 8 {
		return olds
	}
	olds = append(olds, exactOld{text: value, name: name, repl: Label(name)})
	if j := jsonForm(value); j != value {
		olds = append(olds, exactOld{text: j, name: name, repl: Label(name)})
	}
	return olds
}

// Add puts name=value into the exact replacer, so that the value is masked at once. The old
// values of name stay masked. A value of fewer than 8 characters is ignored.
func (r *Redactor) Add(name, value string) {
	if r == nil || utf8.RuneCountInString(value) < 8 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var olds []exactOld
	if cur := r.exact.Load(); cur != nil {
		olds = slices.Clone(cur.olds)
	}
	// A text that the set already has gets the new name and label.
	j := jsonForm(value)
	olds = slices.DeleteFunc(olds, func(o exactOld) bool { return o.text == value || o.text == j })
	r.exact.Store(newExactSet(appendNamed(olds, name, value)))
}

// jsonForm returns the JSON string text of v with no outer quotes and no HTML escapes.
func jsonForm(v string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return v
	}
	out := strings.TrimSuffix(b.String(), "\n")
	return out[1 : len(out)-1]
}

// Mask hides a token. A token of fewer than 18 characters becomes "***". A
// longer token keeps its first 6 and last 4 characters, with "..." between
// them.
func Mask(token string) string {
	n := utf8.RuneCountInString(token)
	if n < 18 {
		return "***"
	}
	if n == len(token) {
		return token[:6] + "..." + token[len(token)-4:]
	}
	r := []rune(token)
	return string(r[:6]) + "..." + string(r[n-4:])
}

// RedactExact returns s with only the known secret values masked, so that the
// backup mask does not change text that only looks like a secret.
func (r *Redactor) RedactExact(s string) string {
	if r == nil || s == "" {
		return s
	}
	set := r.exact.Load()
	if set == nil {
		return s
	}
	return set.repl.Replace(s)
}

// LooksLikeSecret reports whether s contains a known vendor key prefix. The
// warning scan of config.yaml uses it.
func LooksLikeSecret(s string) bool {
	return containsAny(s, prefixLiterals) && prefixRe.MatchString(s)
}

// Redact returns s with the secrets masked. Text with no secrets comes back
// unchanged.
func (r *Redactor) Redact(s string) string {
	return r.redact(s, true)
}

// redactExceptFlags is Redact without the --password and --token form.
func (r *Redactor) redactExceptFlags(s string) string {
	return r.redact(s, false)
}

// redactFlags applies only the --password and --token form.
func redactFlags(s string) string {
	if !strings.Contains(s, "--") {
		return s
	}
	return replaceSubmatch(flagSecretRe, s, func(s string, m []int) string {
		if isMasked(s[m[4]:m[5]]) {
			return s[m[0]:m[1]]
		}
		return s[m[2]:m[3]] + "***"
	})
}

func (r *Redactor) redact(s string, flags bool) string {
	if s == "" {
		return s
	}
	if r != nil {
		if set := r.exact.Load(); set != nil {
			s = set.repl.Replace(s)
		}
	}
	if containsAny(s, prefixLiterals) {
		s = replaceTokens(prefixRe, s)
	}
	if strings.Contains(s, "=") {
		s = redactEnvAssign(s)
	}
	if strings.Contains(s, ":") && strings.Contains(s, `"`) {
		s = replaceSubmatch(jsonFieldRe, s, func(s string, m []int) string {
			if isMasked(s[m[4]:m[5]]) {
				return s[m[0]:m[1]]
			}
			return s[m[2]:m[3]] + `: "` + Mask(s[m[4]:m[5]]) + `"`
		})
	}
	s = replaceSubmatch(authHeaderRe, s, func(s string, m []int) string {
		if isMasked(s[m[4]:m[5]]) {
			return s[m[0]:m[1]]
		}
		return s[m[2]:m[3]] + Mask(s[m[4]:m[5]])
	})
	s = replaceSubmatch(basicAuthRe, s, func(s string, m []int) string {
		if isMasked(s[m[4]:m[5]]) {
			return s[m[0]:m[1]]
		}
		return s[m[2]:m[3]] + "***"
	})
	s = replaceSubmatch(bearerRe, s, func(s string, m []int) string {
		tok := s[m[4]:m[5]]
		if isMasked(tok) || !strings.ContainsAny(tok, "0123456789") {
			return s[m[0]:m[1]]
		}
		return s[m[2]:m[3]] + Mask(tok)
	})
	if strings.Contains(s, ":") {
		s = replaceSubmatch(telegramRe, s, func(s string, m []int) string {
			prefix := ""
			if m[2] >= 0 {
				prefix = s[m[2]:m[3]]
			}
			return prefix + s[m[4]:m[5]] + ":***"
		})
	}
	if strings.Contains(s, "-----BEGIN") {
		s = privateKeyRe.ReplaceAllLiteralString(s, "[REDACTED PRIVATE KEY]")
	}
	if strings.Contains(s, "://") {
		s = replaceSubmatch(dbConnRe, s, func(s string, m []int) string {
			if isMasked(s[m[4]:m[5]]) {
				return s[m[0]:m[1]]
			}
			return s[m[2]:m[3]] + "***" + s[m[6]:m[7]]
		})
	}
	if strings.Contains(s, "://") {
		s = replaceSubmatch(urlUserinfoRe, s, func(s string, m []int) string {
			if isMasked(s[m[4]:m[5]]) {
				return s[m[0]:m[1]]
			}
			return s[m[2]:m[3]] + "***" + s[m[6]:m[7]]
		})
	}
	if strings.Contains(s, "eyJ") {
		s = jwtRe.ReplaceAllStringFunc(s, Mask)
	}
	if flags {
		s = redactFlags(s)
	}
	// Hermes turned off the query parameter redaction. Tether Agent keeps it
	// on.
	if strings.Contains(s, "?") {
		if strings.Contains(s, "://") {
			s = replaceSubmatch(urlQueryRe, s, func(s string, m []int) string {
				frag := ""
				if m[10] >= 0 {
					frag = s[m[10]:m[11]]
				}
				return s[m[2]:m[3]] + "://" + s[m[4]:m[5]] + s[m[6]:m[7]] + "?" + redactQuery(s[m[8]:m[9]]) + frag
			})
		}
		s = replaceSubmatch(httpTargetRe, s, func(s string, m []int) string {
			return s[m[2]:m[3]] + "?" + redactQuery(s[m[4]:m[5]])
		})
	}
	if strings.Contains(s, "&") && strings.Contains(s, "=") {
		s = redactFormBody(s)
	}
	return s
}

// labelOrHandleRe matches a secret label «NAME» and a handle $TMP_SECRET_<n> or ${TMP_SECRET_<n>}.
var labelOrHandleRe = regexp.MustCompile(`^(?:\$TMP_SECRET_[0-9]+|\$\{TMP_SECRET_[0-9]+\}|«[A-Za-z_][A-Za-z0-9_]*»)$`)

// IsLabelOrHandle reports whether v is exactly a secret label «NAME», or a handle
// $TMP_SECRET_<n> or ${TMP_SECRET_<n>}. Redact leaves such a value as it is.
func IsLabelOrHandle(v string) bool {
	return labelOrHandleRe.MatchString(v)
}

// isMasked reports whether a value is already "***", a secret label, or a handle,
// bare or in one pair of quotes. The redactor leaves such a value as it is.
func isMasked(v string) bool {
	if n := len(v); n >= 2 && (v[0] == '"' || v[0] == '\'') && v[n-1] == v[0] {
		v = v[1 : n-1]
	}
	return v == "***" || IsLabelOrHandle(v)
}

// replaceTokens masks group 1 of each prefixRe match. It restarts the scan
// at the end of group 1, because the boundary character after a key can
// also be the boundary character before the next key.
func replaceTokens(re *regexp.Regexp, s string) string {
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		m := re.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		start, end := pos+m[2], pos+m[3]
		b.WriteString(s[last:start])
		b.WriteString(Mask(s[start:end]))
		last, pos = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// redactEnvAssign is Hermes _ENV_ASSIGN_RE with the replacement _redact_env.
// Hermes matches (['"]?)(\S+)\2. Go has no back-reference, so this function
// finds the closing quote: the last quote of the same kind in the value.
func redactEnvAssign(s string) string {
	return replaceSubmatch(envAssignRe, s, func(s string, m []int) string {
		name, run := s[m[2]:m[3]], s[m[4]:m[5]]
		if isMasked(run) {
			return s[m[0]:m[1]]
		}
		if q := run[0]; q == '\'' || q == '"' {
			if k := strings.LastIndexByte(run, q); k >= 2 {
				return name + "=" + string(q) + Mask(run[1:k]) + string(q) + run[k+1:]
			}
		}
		return name + "=" + Mask(run)
	})
}

// redactQuery masks the value of each sensitive key in a k=v&k=v string.
func redactQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, p := range parts {
		k, _, ok := strings.Cut(p, "=")
		if ok && sensitiveQueryParams[strings.ToLower(k)] && !isMasked(p[len(k)+1:]) {
			parts[i] = k + "=***"
		}
	}
	return strings.Join(parts, "&")
}

// redactFormBody masks a form body only when the whole text is one k=v&k=v
// line.
func redactFormBody(s string) string {
	if strings.Contains(s, "\n") {
		return s
	}
	t := strings.TrimSpace(s)
	if !formBodyRe.MatchString(t) {
		return s
	}
	return redactQuery(t)
}

// replaceSubmatch replaces each match of re in s with the result of fn. fn
// gets s and the submatch index pairs of the match.
func replaceSubmatch(re *regexp.Regexp, s string, fn func(s string, m []int) string) string {
	ms := re.FindAllStringSubmatchIndex(s, -1)
	if ms == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		b.WriteString(s[last:m[0]])
		b.WriteString(fn(s, m))
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// literalPrefixes returns the literal start of each pattern, up to the first
// regex meta character.
func literalPrefixes(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if i := strings.IndexAny(p, `[(\.?*+|{^$`); i >= 0 {
			p = p[:i]
		}
		out = append(out, p)
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Match is one span of a text that Redact masks. Kind is "exact", "prefix", or "pattern".
// Name is the .env name of an exact match, or "" when the redactor has no names.
type Match struct {
	Start, End int
	Kind       string
	Name       string
}

// cand is one span that Find may keep. rank is 0 for prefix, then 1, 2, ... in the
// order of redact.
type cand struct {
	m    Match
	rank int
}

// Find returns the spans of s that Redact masks, in the order of Start, with no overlap.
// A label «NAME» and a handle $TMP_SECRET_<n> are not spans.
func (r *Redactor) Find(s string) []Match {
	if s == "" {
		return nil
	}
	var cands []cand
	if set := r.loadExact(); set != nil {
		for _, o := range set.olds {
			for from := 0; ; {
				i := strings.Index(s[from:], o.text)
				if i < 0 {
					break
				}
				st := from + i
				cands = append(cands, cand{m: Match{Start: st, End: st + len(o.text), Kind: "exact", Name: o.name}})
				from = st + 1
			}
		}
	}
	add := func(rank, start, end int) {
		if start < end {
			cands = append(cands, cand{m: Match{Start: start, End: end, Kind: "pattern"}, rank: rank})
		}
	}
	// addValue adds the span of a value, unless the value is masked already.
	addValue := func(rank, start, end int) {
		if !isMasked(s[start:end]) {
			add(rank, start, end)
		}
	}
	if containsAny(s, prefixLiterals) {
		for pos := 0; pos < len(s); {
			m := prefixRe.FindStringSubmatchIndex(s[pos:])
			if m == nil {
				break
			}
			start, end := pos+m[2], pos+m[3]
			cands = append(cands, cand{m: Match{Start: start, End: end, Kind: "prefix"}})
			pos = end
		}
	}
	if strings.Contains(s, "=") {
		for _, m := range envAssignRe.FindAllStringSubmatchIndex(s, -1) {
			if isMasked(s[m[4]:m[5]]) {
				continue
			}
			start, end := m[4], m[5]
			if q := s[start]; q == '\'' || q == '"' {
				if k := strings.LastIndexByte(s[start:end], q); k >= 2 {
					start, end = start+1, start+k
				}
			}
			add(1, start, end)
		}
	}
	if strings.Contains(s, ":") && strings.Contains(s, `"`) {
		for _, m := range jsonFieldRe.FindAllStringSubmatchIndex(s, -1) {
			addValue(2, m[4], m[5])
		}
	}
	for _, m := range authHeaderRe.FindAllStringSubmatchIndex(s, -1) {
		addValue(3, m[4], m[5])
	}
	for _, m := range basicAuthRe.FindAllStringSubmatchIndex(s, -1) {
		addValue(4, m[4], m[5])
	}
	for _, m := range bearerRe.FindAllStringSubmatchIndex(s, -1) {
		if strings.ContainsAny(s[m[4]:m[5]], "0123456789") {
			addValue(5, m[4], m[5])
		}
	}
	if strings.Contains(s, ":") {
		for _, m := range telegramRe.FindAllStringSubmatchIndex(s, -1) {
			add(6, m[6], m[7])
		}
	}
	if strings.Contains(s, "-----BEGIN") {
		for _, m := range privateKeyRe.FindAllStringIndex(s, -1) {
			add(7, m[0], m[1])
		}
	}
	if strings.Contains(s, "://") {
		for _, m := range dbConnRe.FindAllStringSubmatchIndex(s, -1) {
			addValue(8, m[4], m[5])
		}
		for _, m := range urlUserinfoRe.FindAllStringSubmatchIndex(s, -1) {
			addValue(9, m[4], m[5])
		}
	}
	if strings.Contains(s, "eyJ") {
		for _, m := range jwtRe.FindAllStringIndex(s, -1) {
			add(10, m[0], m[1])
		}
	}
	if strings.Contains(s, "--") {
		for _, m := range flagSecretRe.FindAllStringSubmatchIndex(s, -1) {
			start, end := m[4], m[5]
			if isMasked(s[start:end]) {
				continue
			}
			if q := s[start]; (q == '\'' || q == '"') && end-start >= 2 {
				start, end = start+1, end-1
			}
			add(11, start, end)
		}
	}
	if strings.Contains(s, "?") {
		if strings.Contains(s, "://") {
			for _, m := range urlQueryRe.FindAllStringSubmatchIndex(s, -1) {
				cands = appendQuerySpans(cands, s, m[8], m[9], 12)
			}
		}
		for _, m := range httpTargetRe.FindAllStringSubmatchIndex(s, -1) {
			cands = appendQuerySpans(cands, s, m[4], m[5], 13)
		}
	}
	if strings.Contains(s, "&") && strings.Contains(s, "=") && !strings.Contains(s, "\n") {
		t := strings.TrimSpace(s)
		if formBodyRe.MatchString(t) {
			off := strings.Index(s, t)
			cands = appendQuerySpans(cands, s, off, off+len(t), 14)
		}
	}
	return keepSpans(cands)
}

// loadExact returns the exact set of r, or nil.
func (r *Redactor) loadExact() *exactSet {
	if r == nil {
		return nil
	}
	return r.exact.Load()
}

// appendQuerySpans adds the span of each sensitive value in the k=v&k=v text s[start:end].
func appendQuerySpans(cands []cand, s string, start, end, rank int) []cand {
	pos := start
	for p := range strings.SplitSeq(s[start:end], "&") {
		if k, v, ok := strings.Cut(p, "="); ok && v != "" && sensitiveQueryParams[strings.ToLower(k)] && !isMasked(v) {
			vs := pos + len(k) + 1
			cands = append(cands, cand{m: Match{Start: vs, End: vs + len(v), Kind: "pattern"}, rank: rank})
		}
		pos += len(p) + 1
	}
	return cands
}

// keepSpans picks the spans that do not overlap. An exact span wins over each
// other span. Two exact spans follow the strings.Replacer rule. Among the other
// spans, the longer span wins, then the earlier rule.
func keepSpans(cands []cand) []Match {
	if len(cands) == 0 {
		return nil
	}
	slices.SortStableFunc(cands, func(a, b cand) int {
		ae, be := a.m.Kind == "exact", b.m.Kind == "exact"
		switch {
		case ae && be:
			return cmp.Or(cmp.Compare(a.m.Start, b.m.Start), cmp.Compare(b.m.End-b.m.Start, a.m.End-a.m.Start))
		case ae:
			return -1
		case be:
			return 1
		}
		return cmp.Or(cmp.Compare(b.m.End-b.m.Start, a.m.End-a.m.Start), cmp.Compare(a.rank, b.rank), cmp.Compare(a.m.Start, b.m.Start))
	})
	var kept []Match
	for _, c := range cands {
		if !slices.ContainsFunc(kept, func(k Match) bool { return c.m.Start < k.End && k.Start < c.m.End }) {
			kept = append(kept, c.m)
		}
	}
	slices.SortFunc(kept, func(a, b Match) int { return cmp.Compare(a.Start, b.Start) })
	return kept
}
