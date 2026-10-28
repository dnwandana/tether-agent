package security

import (
	"maps"
	"slices"
	"testing"
)

func TestKnownSecretValues(t *testing.T) {
	got := KnownSecretValues(map[string]string{"OPENAI_API_KEY": "sk-from-dotenv-123456"},
		[]string{"PATH=/usr/bin", "GITHUB_TOKEN=ghp_envtoken1234567890", "HOME=/home/x"})
	slices.Sort(got)
	want := []string{"ghp_envtoken1234567890", "sk-from-dotenv-123456"}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
}

func TestKnownSecretNames(t *testing.T) {
	dotenv := map[string]string{"B_KEY": "same-value-123456", "A_KEY": "same-value-123456", "EMPTY": ""}
	got := KnownSecretNames(dotenv, []string{
		"PATH=/usr/bin",
		"GITHUB_TOKEN=ghp_envtoken1234567890",
		// The .env name wins over the environ name for the same value.
		"OTHER_TOKEN=same-value-123456",
	})
	want := map[string]string{"same-value-123456": "A_KEY", "ghp_envtoken1234567890": "GITHUB_TOKEN"}
	if !maps.Equal(got, want) {
		t.Fatal(got)
	}
}

func TestRedactExactSkipsPatterns(t *testing.T) {
	r := NewRedactor([]string{"supersecretvalue-0001", "short"})
	in := "key supersecretvalue-0001 and ghp_abcdefghijklmnopqrst and short"
	want := "key " + Mask("supersecretvalue-0001") + " and ghp_abcdefghijklmnopqrst and short"
	if got := r.RedactExact(in); got != want {
		t.Fatalf("%q", got)
	}
	var nilR *Redactor
	if nilR.RedactExact(in) != in {
		t.Fatal("nil Redactor changed the text")
	}
}

func TestLooksLikeSecret(t *testing.T) {
	for s, want := range map[string]bool{
		"https://x.example/mcp?api_key=sk-abcdefghijklmnopqrst": true,
		"--token ghp_abcdefghijklmnopqrst":                      true,
		"https://api.openai.com/v1":                             false,
		"${OPENAI_API_KEY}":                                     false,
	} {
		if LooksLikeSecret(s) != want {
			t.Errorf("%q: want %v", s, want)
		}
	}
}
