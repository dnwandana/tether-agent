package backup

import (
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
)

func TestRedactConfig(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		dotenv   map[string]string
		env      map[string]string
		contains []string
		absent   []string
		replaced []Replacement
		warnings []string
		same     bool
		wantErr  bool
	}{
		{
			name:     "literal key",
			in:       "providers:\n  openai:\n    # main key\n    api_key: sk-proj-literal-0123456789\n",
			contains: []string{"api_key: ${OPENAI_API_KEY}", "# main key"},
			absent:   []string{"sk-proj-literal-0123456789"},
			replaced: []Replacement{{KeyPath: "providers.openai.api_key", Name: "OPENAI_API_KEY"}},
		},
		{
			name:     "dash in name",
			in:       "providers:\n  open-router:\n    api_key: abcdefgh12345678\n",
			contains: []string{"api_key: ${OPEN_ROUTER_API_KEY}"},
			absent:   []string{"abcdefgh12345678"},
			replaced: []Replacement{{KeyPath: "providers.open-router.api_key", Name: "OPEN_ROUTER_API_KEY"}},
		},
		{
			name:     "reuse .env name",
			in:       "providers:\n  openai:\n    api_key: sk-same-0123456789abc\n",
			dotenv:   map[string]string{"OPENAI_KEY": "sk-same-0123456789abc"},
			contains: []string{"api_key: ${OPENAI_KEY}"},
			absent:   []string{"sk-same-0123456789abc"},
			replaced: []Replacement{{KeyPath: "providers.openai.api_key", Name: "OPENAI_KEY"}},
		},
		{
			name:     "reuse .env name in sorted order",
			in:       "providers:\n  openai:\n    api_key: sk-same-0123456789abc\n",
			dotenv:   map[string]string{"ZED_KEY": "sk-same-0123456789abc", "ALPHA_KEY": "sk-same-0123456789abc"},
			contains: []string{"api_key: ${ALPHA_KEY}"},
			replaced: []Replacement{{KeyPath: "providers.openai.api_key", Name: "ALPHA_KEY"}},
		},
		{
			name:     "name taken",
			in:       "providers:\n  primary:\n    api_key: sk-new-0123456789abcd\n",
			dotenv:   map[string]string{"PRIMARY_API_KEY": "sk-old-0123456789abcd"},
			contains: []string{"api_key: ${PRIMARY_API_KEY_2}"},
			absent:   []string{"sk-new-0123456789abcd"},
			replaced: []Replacement{{KeyPath: "providers.primary.api_key", Name: "PRIMARY_API_KEY_2"}},
		},
		{
			name:     "name taken in the environment",
			in:       "providers:\n  primary:\n    api_key: sk-new-0123456789abcd\n",
			env:      map[string]string{"PRIMARY_API_KEY": "sk-old-0123456789abcd", "PRIMARY_API_KEY_2": "other-value-123"},
			contains: []string{"api_key: ${PRIMARY_API_KEY_3}"},
			replaced: []Replacement{{KeyPath: "providers.primary.api_key", Name: "PRIMARY_API_KEY_3"}},
		},
		{
			name: "shared value",
			in: "providers:\n  openai:\n    api_key: sk-shared-0123456789ab\n" +
				"  backup:\n    api_key: sk-shared-0123456789ab\n",
			contains: []string{"openai:\n    api_key: ${OPENAI_API_KEY}", "backup:\n    api_key: ${OPENAI_API_KEY}"},
			absent:   []string{"sk-shared-0123456789ab"},
			replaced: []Replacement{
				{KeyPath: "providers.openai.api_key", Name: "OPENAI_API_KEY"},
				{KeyPath: "providers.backup.api_key", Name: "OPENAI_API_KEY"},
			},
		},
		{
			name: "same derived name with a different value",
			in: "mcp:\n  servers:\n    a:\n      env:\n        GH_PAT: ghp_first0123456789\n" +
				"    b:\n      env:\n        GH_PAT: ghp_second0123456789\n",
			contains: []string{"GH_PAT: ${GH_PAT}", "GH_PAT: ${GH_PAT_2}"},
			replaced: []Replacement{
				{KeyPath: "mcp.servers.a.env.GH_PAT", Name: "GH_PAT"},
				{KeyPath: "mcp.servers.b.env.GH_PAT", Name: "GH_PAT_2"},
			},
		},
		{
			name: "reference",
			in:   "mcp:\n  servers:\n    gh:\n      headers:\n        Authorization: Bearer ${GITHUB_TOKEN}\n",
			same: true,
		},
		{
			name:     "mcp env",
			in:       "mcp:\n  servers:\n    gh:\n      env:\n        GH_PAT: ghp_literal0123456789\n",
			contains: []string{"GH_PAT: ${GH_PAT}"},
			absent:   []string{"ghp_literal0123456789"},
			replaced: []Replacement{{KeyPath: "mcp.servers.gh.env.GH_PAT", Name: "GH_PAT"}},
		},
		{
			name:     "mcp header",
			in:       "mcp:\n  servers:\n    github:\n      headers:\n        Authorization: Bearer abc0123456789xyz\n",
			contains: []string{"Authorization: ${MCP_GITHUB_AUTHORIZATION}"},
			absent:   []string{"abc0123456789xyz"},
			replaced: []Replacement{{KeyPath: "mcp.servers.github.headers.Authorization", Name: "MCP_GITHUB_AUTHORIZATION"}},
		},
		{
			name:     "bot token",
			in:       "telegram:\n  bot_token: 123456:literal-token-abcdef\n",
			contains: []string{"bot_token: ${TELEGRAM_BOT_TOKEN}"},
			absent:   []string{"literal-token-abcdef"},
			replaced: []Replacement{{KeyPath: "telegram.bot_token", Name: "TELEGRAM_BOT_TOKEN"}},
		},
		{
			name:     "known value elsewhere",
			in:       "web:\n  searxng_url: https://u:knownvalue123@x\n",
			dotenv:   map[string]string{"SEARX": "knownvalue123"},
			contains: []string{"https://u:${SEARX}@x"},
			absent:   []string{"knownvalue123"},
			replaced: []Replacement{{KeyPath: "web.searxng_url", Name: "SEARX"}},
		},
		{
			name:     "two known values in one string",
			in:       "mcp:\n  servers:\n    x:\n      args: [\"--user=user-0123456789\", \"--key=key-abcdefghij --user=user-0123456789\"]\n",
			dotenv:   map[string]string{"X_USER": "user-0123456789", "X_KEY": "key-abcdefghij"},
			contains: []string{"--user=${X_USER}", "--key=${X_KEY} --user=${X_USER}"},
			absent:   []string{"user-0123456789", "key-abcdefghij"},
			replaced: []Replacement{
				{KeyPath: "mcp.servers.x.args[0]", Name: "X_USER"},
				{KeyPath: "mcp.servers.x.args[1]", Name: "X_KEY"},
				{KeyPath: "mcp.servers.x.args[1]", Name: "X_USER"},
			},
		},
		{
			name:   "short known value stays",
			in:     "model: gpt-5\n",
			dotenv: map[string]string{"MODEL": "gpt-5"},
			same:   true,
		},
		{
			name:     "warning only",
			in:       "mcp:\n  servers:\n    search:\n      url: https://x/mcp?api_key=sk-abcdefghijklmnopqrst\n",
			same:     true,
			warnings: []string{"mcp.servers.search.url"},
		},
		{
			name: "no change",
			in: "# Tether Agent config\nproviders:\n  openai:\n    api_key: ${OPENAI_API_KEY}   # from .env\n" +
				"telegram:\n    bot_token: \"${TELEGRAM_BOT_TOKEN}\"\nmodel:  gpt-5\n",
			same: true,
		},
		{
			name: "empty value and null",
			in:   "providers:\n  openai:\n    api_key: \"\"\n  local:\n    api_key: null\n",
			same: true,
		},
		{
			name: "empty file",
			in:   "",
			same: true,
		},
		{
			name:    "bad yaml",
			in:      "providers: [",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(name string) (string, bool) {
				v, ok := tt.env[name]
				return v, ok
			}
			known := security.KnownSecretNames(tt.dotenv, nil)
			got, err := redactConfig([]byte(tt.in), tt.dotenv, lookup, known)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("redactConfig: want an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("redactConfig: %v", err)
			}
			out := string(got.Data)
			if tt.same && out != tt.in {
				t.Errorf("Data changed:\n got: %q\nwant: %q", out, tt.in)
			}
			for _, s := range tt.contains {
				if !strings.Contains(out, s) {
					t.Errorf("Data does not contain %q:\n%s", s, out)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(out, s) {
					t.Errorf("Data contains %q:\n%s", s, out)
				}
			}
			if !slices.Equal(got.Replaced, tt.replaced) {
				t.Errorf("Replaced = %v, want %v", got.Replaced, tt.replaced)
			}
			if !slices.Equal(got.Warnings, tt.warnings) {
				t.Errorf("Warnings = %v, want %v", got.Warnings, tt.warnings)
			}
		})
	}
}

func TestRedactConfigNilLookup(t *testing.T) {
	in := "telegram:\n  bot_token: 123456:literal-token-abcdef\n"
	got, err := redactConfig([]byte(in), nil, nil, nil)
	if err != nil {
		t.Fatalf("redactConfig: %v", err)
	}
	if !strings.Contains(string(got.Data), "${TELEGRAM_BOT_TOKEN}") {
		t.Errorf("Data = %q, want the reference", got.Data)
	}
}

func TestRedactConfigErrorHasNoValue(t *testing.T) {
	in := "providers:\n  openai:\n    api_key: sk-secret-0123456789abc\n  x: [\n"
	_, err := redactConfig([]byte(in), nil, nil, nil)
	if err == nil {
		t.Fatal("redactConfig: want an error, got nil")
	}
	if strings.Contains(err.Error(), "sk-secret-0123456789abc") {
		t.Errorf("error text holds the secret: %v", err)
	}
}
