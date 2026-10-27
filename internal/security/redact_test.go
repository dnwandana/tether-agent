package security

import (
	"strings"
	"testing"
)

func TestMaskRule(t *testing.T) {
	if Mask("shorttoken123") != "***" {
		t.Fatal()
	}
	if Mask("sk-abcdefghijklmnopqrstuv") != "sk-abc...stuv" {
		t.Fatal(Mask("sk-abcdefghijklmnopqrstuv"))
	}
}

func TestMaskEdges(t *testing.T) {
	if Mask("") != "***" {
		t.Fatal("empty token must be ***")
	}
	if got := Mask(strings.Repeat("a", 17)); got != "***" {
		t.Fatal(got)
	}
	if got := Mask("abcdef" + strings.Repeat("x", 8) + "wxyz"); got != "abcdef...wxyz" {
		t.Fatal(got)
	}
	// Mask counts runes, so it never cuts a multi-byte character.
	if got := Mask("ééééééxxxxxxxxxxxxxxéééé"); got != "éééééé...éééé" {
		t.Fatal(got)
	}
}

func TestRedactsVendorKeys(t *testing.T) {
	r := NewRedactor(nil)
	in := "key sk-proj-ABCDEFGHIJKLMNOPQRSTUV and ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123 and Bearer eyJhbGciOiJIUzI1NiJ9.e30.abcdefghijk"
	out := r.Redact(in)
	for _, leak := range []string{"ABCDEFGHIJKLMNOPQRSTUV", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123", "eyJhbGciOiJIUzI1NiJ9.e30.abcdefghijk"} {
		if strings.Contains(out, leak) {
			t.Fatalf("leak %s in %s", leak, out)
		}
	}
}

func TestRedactsSensitiveQueryParams(t *testing.T) {
	out := NewRedactor(nil).Redact("GET https://x.io/a?access_token=abcdef123456&q=ok&api_key=zzz")
	if strings.Contains(out, "abcdef123456") || strings.Contains(out, "api_key=zzz") || !strings.Contains(out, "q=ok") {
		t.Fatal(out)
	}
}

func TestRedactsExactDotEnvValues(t *testing.T) {
	out := NewRedactor([]string{"my-helius-value-0001"}).Redact("using my-helius-value-0001 now")
	if strings.Contains(out, "my-helius-value-0001") {
		t.Fatal(out)
	}
}

func TestTelegramBotTokenRedacted(t *testing.T) {
	out := NewRedactor(nil).Redact("https://api.telegram.org/bot123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw/getMe")
	if strings.Contains(out, "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw") {
		t.Fatal(out)
	}
}

func TestExactValuesRules(t *testing.T) {
	// Values shorter than 8 characters are not replaced. They match too much.
	r := NewRedactor([]string{"short", "", "abcdefgh-long-secret-value", "abcdefgh"})
	out := r.Redact("short words and abcdefgh-long-secret-value and abcdefgh")
	if !strings.Contains(out, "short words") {
		t.Fatal(out)
	}
	// The longer value wins where two values overlap.
	if strings.Contains(out, "-long-secret-value") || strings.Contains(out, " abcdefgh") {
		t.Fatal(out)
	}
}

func TestNilRedactorStillRedactsPatterns(t *testing.T) {
	var r *Redactor
	if out := r.Redact("key sk-proj-abc123def456ghi789jkl012"); strings.Contains(out, "abc123def456") {
		t.Fatal(out)
	}
}

// The cases below are ported from Hermes tests/agent/test_redact.py.

type redactCase struct {
	name    string
	in      string
	gone    []string // substrings that must not stay in the output
	kept    []string // substrings that must stay in the output
	same    bool     // the output must equal the input
	hasMask bool     // the output must contain "..."
}

func runRedactCases(t *testing.T, cases []redactCase) {
	t.Helper()
	r := NewRedactor(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := r.Redact(c.in)
			if c.same && out != c.in {
				t.Fatalf("changed:\n in: %q\nout: %q", c.in, out)
			}
			for _, g := range c.gone {
				if strings.Contains(out, g) {
					t.Fatalf("leak %q in %q", g, out)
				}
			}
			for _, k := range c.kept {
				if !strings.Contains(out, k) {
					t.Fatalf("missing %q in %q", k, out)
				}
			}
			if c.hasMask && !strings.Contains(out, "...") {
				t.Fatalf("no partial mask in %q", out)
			}
		})
	}
}

func TestHermesKnownPrefixes(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "openai", in: "Using key sk-proj-abc123def456ghi789jkl012", gone: []string{"abc123def456"}, kept: []string{"sk-pro"}, hasMask: true},
		{name: "openrouter", in: "OPENROUTER_API_KEY=sk-or-v1-abcdefghijklmnopqrstuvwxyz1234567890", gone: []string{"abcdefghijklmnop"}},
		{name: "github classic", in: "token: ghp_abc123def456ghi789jkl", gone: []string{"abc123def456"}},
		{name: "github fine grained", in: "github_pat_abc123def456ghi789jklmno", gone: []string{"abc123def456"}},
		{name: "slack", in: "xoxb-" + strings.Repeat("0", 12) + "-" + strings.Repeat("a", 14), gone: []string{strings.Repeat("a", 14)}},
		{name: "google", in: "AIzaSyB-abc123def456ghi789jklmno012345", gone: []string{"abc123def456"}},
		{name: "perplexity", in: "pplx-abcdef123456789012345", gone: []string{"abcdef12345"}},
		{name: "fal", in: "fal_abc123def456ghi789jkl", gone: []string{"abc123def456"}},
		{name: "short token", in: "key=sk-short1234567", kept: []string{"***"}},
		{name: "aws", in: "id AKIAABCDEFGHIJKLMNOP end", gone: []string{"ABCDEFGHIJKLMNOP"}},
		{name: "anthropic", in: "sk-ant-api03-abcdefghijklmnopqrstuvwxyz", gone: []string{"abcdefghijklmnopqrstuvwxyz"}},
		{name: "elevenlabs env", in: "ELEVENLABS_API_KEY=sk_abc123def456ghi789jklmnopqrstu", gone: []string{"abc123def456ghi"}},
		{name: "elevenlabs log", in: "Connecting to ElevenLabs with key sk_abc123def456ghi789jklmnopqrstu", gone: []string{"abc123def456ghi"}},
		{name: "tavily env", in: "TAVILY_API_KEY=tvly-ABCdef123456789GHIJKL0000", gone: []string{"ABCdef123456789"}},
		{name: "tavily log", in: "Initialising Tavily client with tvly-ABCdef123456789GHIJKL0000", gone: []string{"ABCdef123456789"}},
		{name: "exa env", in: "EXA_API_KEY=exa_XYZ789abcdef000000000000000", gone: []string{"XYZ789abcdef"}},
		{name: "exa log", in: "Using Exa client with key exa_XYZ789abcdef000000000000000", gone: []string{"XYZ789abcdef"}},
		{name: "xai bare", in: "using key xai-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstu", gone: []string{"xai-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstu"}, kept: []string{"xai-AB"}},
		{name: "xai env", in: "XAI_API_KEY=xai-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstu", gone: []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"}},
		{name: "xai too short", in: "text xai-tooshort here", same: true},
		{name: "xai company", in: "xai is a company", same: true},
	})
}

func TestPrefixBoundaries(t *testing.T) {
	runRedactCases(t, []redactCase{
		// A prefix inside a longer word is not a key.
		{name: "sk_ inside word", in: "the task_abcdefghijklmnop runs", same: true},
		{name: "sk- inside word", in: "a risk-assessmentframework doc", same: true},
		{name: "two keys", in: "sk-aaaaaaaaaaaaaaaaaaaa sk-bbbbbbbbbbbbbbbbbbbb", gone: []string{"aaaaaaaaaaaa", "bbbbbbbbbbbb"}},
		{name: "key in parens", in: "(sk-aaaaaaaaaaaaaaaaaaaa)", gone: []string{"aaaaaaaaaaaa"}},
		// Hermes falls back from sk_live_ to sk_ when an underscore follows.
		{name: "sk_live_ fallback", in: "k sk_live_abcdefghijkl_mnop end", gone: []string{"abcdefghijkl"}},
	})
}

func TestHermesEnvAssignments(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "export key", in: "export OPENAI_API_KEY=sk-proj-abc123def456ghi789jkl012", gone: []string{"abc123def456"}, kept: []string{"OPENAI_API_KEY="}},
		{name: "quoted", in: `MY_SECRET_TOKEN="supersecretvalue123456789"`, gone: []string{"supersecretvalue"}, kept: []string{`MY_SECRET_TOKEN="`}},
		{name: "quoted then text", in: `MY_TOKEN="supersecretvalue123456789" next`, gone: []string{"supersecretvalue"}, kept: []string{`" next`}},
		{name: "home", in: "HOME=/home/user", same: true},
		{name: "path", in: "PATH=/usr/local/bin:/usr/bin", same: true},
		{name: "python tokens", in: "before_tokens = response.usage.prompt_tokens", same: true},
		{name: "python api_key", in: "api_key = config.get('api_key')", same: true},
		{name: "ts await token", in: "const token = await getToken();", same: true},
		{name: "ts await secret", in: "const secret = await fetchSecret();", same: true},
		{name: "export whitespace", in: "export SECRET_TOKEN=mypassword", gone: []string{"mypassword"}, kept: []string{"export SECRET_TOKEN="}},
	})
}

func TestHermesJSONFields(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "apiKey", in: `{"apiKey": "sk-proj-abc123def456ghi789jkl012"}`, gone: []string{"abc123def456"}},
		{name: "access_token", in: `{"access_token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.longtoken.here"}`, gone: []string{"eyJhbGciOiJSUzI1NiIs"}},
		{name: "not secret", in: `{"name": "John", "model": "gpt-4"}`, same: true},
		{name: "secret_value", in: `{"success": true, "secret_value": "sk-test-secret-1234567890"}`, gone: []string{"sk-test-secret-1234567890"}},
		{name: "raw_secret", in: `{"raw_secret": "ghp_abc123def456ghi789jkl"}`, gone: []string{"abc123def456"}},
		{name: "token_count is not secret", in: `{"token_count": "12345"}`, same: true},
	})
}

func TestHermesAuthHeaders(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "bearer", in: "Authorization: Bearer sk-proj-abc123def456ghi789jkl012", gone: []string{"abc123def456"}, kept: []string{"Authorization: Bearer"}},
		{name: "lower case", in: "authorization: bearer mytoken123456789012345678", gone: []string{"mytoken12345"}},
		{name: "bare bearer", in: "sent Bearer opaque0123456789abcdefXYZ now", gone: []string{"opaque0123456789abcdefXYZ"}, kept: []string{"Bearer ", " now"}},
		{name: "bearer prose", in: "the Bearer authentication scheme uses a token", same: true},
	})
}

func TestHermesTelegramTokens(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "bot token", in: "bot123456789:ABCDEfghij-KLMNopqrst_UVWXyz12345", gone: []string{"ABCDEfghij"}, kept: []string{"123456789:***"}},
		{name: "raw token", in: "12345678901:ABCDEfghijKLMNopqrstUVWXyz1234567890", gone: []string{"ABCDEfghij"}},
		{name: "time is not a token", in: "at 12:30:45 the job ran", same: true},
	})
}

func TestHermesPassthrough(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "empty", in: "", same: true},
		{name: "normal", in: "Hello world, this is a normal log message with no secrets.", same: true},
		{name: "code", in: "def main():\n    print('hello')\n    return 42", same: true},
		{name: "url", in: "Connecting to https://api.openai.com/v1/chat/completions", same: true},
		{name: "discord mention", in: "Hello <@222589316709220353>", same: true},
		{name: "discord nick mention", in: "Ping <@!1331549159177846844>", same: true},
		{name: "two mentions", in: "<@111111111111111111> and <@222222222222222222>", same: true},
		{name: "slack mention", in: "<@U024BE7LH>", same: true},
		{name: "short eyJ", in: "eyJust a normal word", same: true},
		{name: "ordinary words", in: "The key to the token economy is a secret password policy.", same: true},
	})
}

func TestHermesPrintenvSimulation(t *testing.T) {
	dump := `HOME=/home/user
PATH=/usr/local/bin:/usr/bin
OPENAI_API_KEY=sk-proj-abc123def456ghi789jkl012mno345
OPENROUTER_API_KEY=sk-or-v1-reallyLongSecretKeyValue12345678
FIRECRAWL_API_KEY=fc-shortkey123456789012
TELEGRAM_BOT_TOKEN=bot987654321:ABCDEfghij-KLMNopqrst_UVWXyz12345
ELEVENLABS_API_KEY=sk_abc123def456ghi789jklmnopqrstu
TAVILY_API_KEY=tvly-ABCdef123456789GHIJKL0000
EXA_API_KEY=exa_XYZ789abcdef000000000000000
SHELL=/bin/bash
USER=user`
	runRedactCases(t, []redactCase{{
		name: "env dump",
		in:   dump,
		gone: []string{"abc123def456", "reallyLongSecretKey", "ABCDEfghij", "shortkey123", "ABCdef123456789", "XYZ789abcdef"},
		kept: []string{"HOME=/home/user", "SHELL=/bin/bash", "USER=user", "PATH=/usr/local/bin:/usr/bin"},
	}})
}

func TestHermesJWT(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "three parts", in: "Token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiI0MjNiZDJkYjg4MjI0MDAwIn0.Gxgv0rru-_kS-I_60EJ7CENTnBh9UeuL3QhkMoQ-VnM", gone: []string{"eyJpc3Mi", "Gxgv0rru"}, kept: []string{"Token:"}},
		{name: "two parts", in: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0", gone: []string{"eyJzdWIi"}},
		{name: "header only", in: "leaked header: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9 here", gone: []string{"IkpXVCJ9"}, kept: []string{"leaked header:"}},
		{name: "padding", in: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0=.abc123def456ghij", gone: []string{"abc123def456"}},
		{name: "surrounding text", in: "before eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0 after", kept: []string{"before ", " after"}},
		{name: "home assistant", in: "Home Assistant API Token: eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJhYmNkZWYiLCJleHAiOjE3NzQ5NTcxMDN9.Gxgv0rru-_kS-I_60EJ7CENTnBh9UeuL3QhkMoQ-VnM", gone: []string{"Gxgv0rru"}, kept: []string{"Home Assistant API Token:"}, hasMask: true},
	})
}

// Hermes turned web URL redaction off. Tether Agent turns it on, so the
// cases with sensitive parameters are reversed here.
func TestURLs(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "oauth code", in: "GET https://api.example.com/oauth/cb?code=abc123xyz789&state=csrf_ok", gone: []string{"abc123xyz789"}, kept: []string{"code=***", "state=csrf_ok"}},
		{name: "access token", in: "Fetching https://example.com/api?access_token=opaque_value_here_1234&format=json", gone: []string{"opaque_value_here_1234"}, kept: []string{"format=json"}},
		{name: "magic link", in: "Open https://checkout.example.com/resume?magic=ABCDEF123456&customer=42", same: true},
		{name: "presigned", in: "https://s3.amazonaws.com/bucket/k?signature=LONG_PRESIGNED_SIG&id=public", gone: []string{"LONG_PRESIGNED_SIG"}, kept: []string{"id=public"}},
		{name: "fragment kept", in: "https://x.io/p?token=abc#frag", gone: []string{"token=abc"}, kept: []string{"#frag"}},
		{name: "mixed case name", in: "https://x.io/p?Api_Key=abc&x=1", gone: []string{"Api_Key=abc"}, kept: []string{"x=1"}},
		{name: "userinfo", in: "URL: https://user:supersecretpw@host.example.com/path", same: true},
		{name: "websocket", in: "wss://api.example.com/ws?token=opaqueWsToken123", gone: []string{"opaqueWsToken123"}},
		{name: "access log", in: `INFO aiohttp.access: 127.0.0.1 "POST /bluebubbles-webhook?password=webhookSecret123&event=new-message HTTP/1.1" 200 173 "-" "test-client"`, gone: []string{"webhookSecret123"}, kept: []string{"event=new-message", "HTTP/1.1"}},
		{name: "prefix in url", in: "https://evil.com/steal?q=sk-" + strings.Repeat("a", 30), gone: []string{"sk-" + strings.Repeat("a", 30)}},
		{name: "db connstr", in: "postgres://admin:dbpass@db.internal:5432/app", gone: []string{"dbpass"}, kept: []string{"postgres://admin:***@db.internal"}},
		{name: "mongo srv", in: "mongodb+srv://u:p4ss@cluster0.x.net/db", gone: []string{"p4ss"}},
	})
}

func TestHermesFormBody(t *testing.T) {
	runRedactCases(t, []redactCase{
		{name: "pure form", in: "password=mysecret&username=bob&token=opaqueValue", gone: []string{"mysecret", "opaqueValue"}, kept: []string{"username=bob"}},
		{name: "oauth request", in: "grant_type=password&client_id=app&client_secret=topsecret&username=alice&password=alicepw", gone: []string{"topsecret", "alicepw"}, kept: []string{"client_id=app"}},
		{name: "not a form", in: "I have password=foo and other things", kept: []string{"I have"}},
		{name: "multi line", in: "first=1\nsecond=2", kept: []string{"first=1"}},
	})
}

func TestPrivateKeyBlock(t *testing.T) {
	in := "before\n-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234\nabcd\n-----END RSA PRIVATE KEY-----\nafter"
	runRedactCases(t, []redactCase{{
		name: "rsa",
		in:   in,
		gone: []string{"MIIEpAIBAAKCAQEA1234"},
		kept: []string{"before\n", "[REDACTED PRIVATE KEY]", "\nafter"},
	}})
}
