package config

import (
	"slices"
	"strings"
	"testing"
)

func TestOutboundDefaults(t *testing.T) {
	o := Default().Outbound
	if !o.Scan || !o.Guard || o.GuardProvider != "" || o.PrivateChats == nil || len(o.PrivateChats) != 0 {
		t.Fatalf("%+v", o)
	}
}

func TestOutboundLoad(t *testing.T) {
	c, err := Load(writeTemp(t, minimal+"  guard:\n    format: openai\n    base_url: u\n    api_key: k\n    model: g\n"+
		"outbound:\n  scan: false\n  private_chats: [\"-1001234567890\", \"42\"]\n  guard: false\n  guard_provider: guard\n"),
		mapEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	o := c.Outbound
	if o.Scan || o.Guard || o.GuardProvider != "guard" || !slices.Equal(o.PrivateChats, []string{"-1001234567890", "42"}) {
		t.Fatalf("%+v", o)
	}
}

func TestValidateGuardProvider(t *testing.T) {
	c := Default()
	c.Providers["primary"] = Provider{Format: "openai"}
	c.Outbound.GuardProvider = "nope"
	want := `config: outbound.guard_provider names "nope", which is not in providers`
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	for _, name := range []string{"", "primary", "guard"} {
		c := Default()
		c.Providers["primary"] = Provider{Format: "openai"}
		c.Providers["guard"] = Provider{Format: "anthropic"}
		c.Outbound.GuardProvider = name
		if err := c.Validate(); err != nil {
			t.Fatalf("guard_provider %q must be valid: %v", name, err)
		}
	}
}
