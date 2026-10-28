package security

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
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

	// jwtRe is Hermes _JWT_RE.
	jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_=-]{4,}){0,2}`)

	// urlQueryRe is Hermes _URL_WITH_QUERY_RE.
	urlQueryRe = regexp.MustCompile(`(https?|wss?|ftp)://([^\s/?#]+)([^\s?#]*)\?([^\s#]+)(#\S*)?`)

	// httpTargetRe is Hermes _HTTP_REQUEST_TARGET_QUERY_RE.
	httpTargetRe = regexp.MustCompile(`(?i)\b((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|TRACE|CONNECT)\s+[^ \t\r\n"']*?)\?([^ \t\r\n"']+)`)

	// formBodyRe is Hermes _FORM_BODY_RE.
	formBodyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*=[^&\s]*(?:&[A-Za-z_][A-Za-z0-9_.-]*=[^&\s]*)+$`)
)

// Redactor masks API keys, tokens, and credentials in text. It is safe for
// concurrent use. A nil *Redactor applies the patterns only.
type Redactor struct {
	exact *strings.Replacer
}

// NewRedactor returns a Redactor that also masks each value in exactValues.
// exactValues is the list of values from .env. Values shorter than 8
// characters are ignored, because they match too much ordinary text.
func NewRedactor(exactValues []string) *Redactor {
	vals := make([]string, 0, len(exactValues))
	for _, v := range exactValues {
		if utf8.RuneCountInString(v) >= 8 {
			vals = append(vals, v)
		}
	}
	// strings.Replacer tries the old strings in argument order, so the
	// longest value goes first and wins where two values overlap.
	slices.SortFunc(vals, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	vals = slices.Compact(vals)
	r := &Redactor{}
	if len(vals) > 0 {
		pairs := make([]string, 0, 2*len(vals))
		for _, v := range vals {
			pairs = append(pairs, v, Mask(v))
		}
		r.exact = strings.NewReplacer(pairs...)
	}
	return r
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
	if r == nil || r.exact == nil || s == "" {
		return s
	}
	return r.exact.Replace(s)
}

// LooksLikeSecret reports whether s contains a known vendor key prefix. The
// warning scan of config.yaml uses it.
func LooksLikeSecret(s string) bool {
	return containsAny(s, prefixLiterals) && prefixRe.MatchString(s)
}

// Redact returns s with the secrets masked. Text with no secrets comes back
// unchanged.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	if r != nil && r.exact != nil {
		s = r.exact.Replace(s)
	}
	if containsAny(s, prefixLiterals) {
		s = replaceTokens(prefixRe, s)
	}
	if strings.Contains(s, "=") {
		s = redactEnvAssign(s)
	}
	if strings.Contains(s, ":") && strings.Contains(s, `"`) {
		s = replaceSubmatch(jsonFieldRe, s, func(s string, m []int) string {
			return s[m[2]:m[3]] + `: "` + Mask(s[m[4]:m[5]]) + `"`
		})
	}
	s = replaceSubmatch(authHeaderRe, s, func(s string, m []int) string {
		return s[m[2]:m[3]] + Mask(s[m[4]:m[5]])
	})
	s = replaceSubmatch(bearerRe, s, func(s string, m []int) string {
		tok := s[m[4]:m[5]]
		if !strings.ContainsAny(tok, "0123456789") {
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
			return s[m[2]:m[3]] + "***" + s[m[6]:m[7]]
		})
	}
	if strings.Contains(s, "eyJ") {
		s = jwtRe.ReplaceAllStringFunc(s, Mask)
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
		if ok && sensitiveQueryParams[strings.ToLower(k)] {
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
