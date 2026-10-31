package setup

import (
	"strings"
	"testing"
)

func TestConfigDocDelete(t *testing.T) {
	c := mustConfigDoc(t, "# top\nproviders:\n  primary:\n    format: openai # the old format\n    base_url: https://x/v1\n    model: m # my model\n")
	c.delete("providers", "primary", "format")
	c.delete("providers", "primary", "base_url")
	// An absent key or path is no error.
	c.delete("providers", "primary", "format")
	c.delete("providers", "nothing", "format")
	c.delete("agent")
	b, err := c.bytes()
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "format") || strings.Contains(got, "base_url") ||
		!strings.Contains(got, "# top") || !strings.Contains(got, "model: m # my model") {
		t.Fatal(got)
	}
}
