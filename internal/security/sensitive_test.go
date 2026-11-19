package security

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func TestScanDetectors(t *testing.T) {
	for _, tc := range []struct{ text, kind, label string }{
		{"Pay 4111 1111 1111 1111 now", "card", "[card ••••1111]"},
		{"Pay 4111-1111-1111-1111 now", "card", "[card ••••1111]"},
		{"Pay 5555555555554444 now", "card", "[card ••••4444]"},
		{"Amex 378282246310005.", "card", "[card ••••0005]"},
		{"Card 4111 1111 1111 1111 22", "card", "[card ••••1111]"},
		// A card can start at a later digit group of the run.
		{"Order 12 4111 1111 1111 1111 today.", "card", "[card ••••1111]"},
		{"On 2026-11-19 4111-1111-1111-1111 was charged", "card", "[card ••••1111]"},
		{"Ids 99 88 4111 1111 1111 1111", "card", "[card ••••1111]"},
		{"Ref 1-5555555555554444.", "card", "[card ••••4444]"},
		{"Qty 7 378282246310005 22", "card", "[card ••••0005]"},
		{"IBAN GB82 WEST 1234 5698 7654 32 ok", "iban", "[IBAN GB82••••5432]"},
		{"IBAN DE89370400440532013000", "iban", "[IBAN DE89••••3000]"},
		{`{"iban":"x\nGB82WEST12345698765432"}`, "iban", "[IBAN GB82••••5432]"},
		{"Call +94 77 123 4567 today", "phone", "[phone +94•••••4567]"},
		{"Call +1 (415) 555-0100.", "phone", "[phone +14•••••0100]"},
		{"Call +44.20.7946.0958", "phone", "[phone +44•••••0958]"},
	} {
		f := Scan(tc.text, nil, nil)
		if len(f) != 1 || f[0].Kind != tc.kind || f[0].Label != tc.label {
			t.Errorf("%q: got %+v, want one %s %s", tc.text, f, tc.kind, tc.label)
		}
	}
}

func TestScanNoMatch(t *testing.T) {
	for _, text := range []string{
		"Order 4111 1111 1111 1112",        // the Luhn check fails
		"Order 1234567890123",              // the first digit is 1
		"Run 12345678904111111111111111",   // part of a longer digit run
		"Run 1 12345678904111111111111111", // part of a longer digit run after a group
		"Date 2026-11-18, version 1.26.3",  //
		"Call 077 123 4567",                // no +
		"Call +12345",                      // too short
		"IBAN GB82 WEST 1234 5698 7654 33", // mod-97 fails
		"IBAN GB82WEST123456987654",        // a wrong length for GB
		"XX82WEST12345698765432",           // no such country
		"«PYPI_TOKEN» and $TMP_SECRET_3",
		"[card ••••1111] [IBAN GB82••••5432] [phone +94•••••4567]",
	} {
		if f := Scan(text, nil, nil); len(f) != 0 {
			t.Errorf("%q: got %+v", text, f)
		}
	}
}

// TestScanCardAfterGroup checks that a card after a leading digit group is masked in full.
func TestScanCardAfterGroup(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Order 12 4111 1111 1111 1111 today.", "Order 12 [card ••••1111] today."},
		{"On 2026-11-19 4111-1111-1111-1111 was charged", "On 2026-11-19 [card ••••1111] was charged"},
		{"Pay 4111 1111 1111 1111 and 12 5555 5555 5555 4444", "Pay [card ••••1111] and 12 [card ••••4444]"},
	} {
		if got := RedactFindings(tc.text, Scan(tc.text, nil, nil)); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.text, got, tc.want)
		}
	}
	f, red := ScanArgs(json.RawMessage(`{"body":"Order 12 4111 1111 1111 1111 today."}`), nil, nil)
	if len(f) != 1 || f[0].Kind != "card" || strings.Contains(string(red), "4111") {
		t.Fatalf("%+v %s", f, red)
	}
}

// TestScanCardAfterLetter checks that a digit run inside a token is not a card, and that a
// letter after a backslash does not hide a card.
func TestScanCardAfterLetter(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"hash x4111111111111111 end", "hash x4111111111111111 end"},
		{"id ab4111 1111 1111 1111", "id ab4111 1111 1111 1111"},
		{`line\n4111111111111111`, `line\n[card ••••1111]`},
		{"pay (4111111111111111)", "pay ([card ••••1111])"},
	} {
		if got := RedactFindings(tc.text, Scan(tc.text, nil, nil)); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestScanSecretsAndOverlap(t *testing.T) {
	r := NewRedactor([]string{"supersecretvalue123"})
	if f := Scan("token supersecretvalue123 ok", r, nil); len(f) != 1 || f[0].Kind != "secret" || f[0].Label != "[secret]" {
		t.Fatalf("%+v", f)
	}
	if f := Scan("x hv-abcdefgh1234 y", nil, []string{"hv-abcdefgh1234", "short"}); len(f) != 1 || f[0].Label != "[secret]" {
		t.Fatalf("%+v", f)
	}
	// The secret covers the card digits and is longer, so it wins.
	f := Scan("key sk-4111111111111111abc end", r, nil)
	if len(f) != 1 || f[0].Kind != "secret" {
		t.Fatalf("%+v", f)
	}
}

func TestScanNamedSecretLabel(t *testing.T) {
	r := NewNamedRedactor(map[string]string{"PYPI_TOKEN": "pypi-AgEIcHlwaS5vcmc0123456789"})
	f := Scan("use pypi-AgEIcHlwaS5vcmc0123456789 now", r, nil)
	if len(f) != 1 || f[0].Label != "«PYPI_TOKEN»" {
		t.Fatalf("%+v", f)
	}
}

func TestRedactFindings(t *testing.T) {
	text := "Pay 4111 1111 1111 1111 or call +94 77 123 4567."
	got := RedactFindings(text, Scan(text, nil, nil))
	if got != "Pay [card ••••1111] or call [phone +94•••••4567]." {
		t.Fatal(got)
	}
	if RedactFindings(text, nil) != text {
		t.Fatal("no finding changes nothing")
	}
}

func TestScanArgs(t *testing.T) {
	args := json.RawMessage(`{"to":"+94 77 123 4567","body":{"lines":["card 4111 1111 1111 1111","call +94 77 123 4567"]},"n":3}`)
	f, red := ScanArgs(args, nil, nil)
	if !maps.Equal(FindingKinds(f), map[string]int{"card": 1, "phone": 1}) {
		t.Fatalf("%+v", f)
	}
	var v struct {
		To   string `json:"to"`
		Body struct {
			Lines []string `json:"lines"`
		} `json:"body"`
		N int `json:"n"`
	}
	if err := json.Unmarshal(red, &v); err != nil {
		t.Fatal(err)
	}
	if v.To != "+94 77 123 4567" || v.Body.Lines[0] != "card [card ••••1111]" ||
		v.Body.Lines[1] != "call [phone +94•••••4567]" || v.N != 3 {
		t.Fatalf("%+v", v)
	}
	if f, red := ScanArgs(json.RawMessage(`{"to":"+94 77 123 4567","text":"hello"}`), nil, nil); f != nil || red != nil {
		t.Fatal(f, string(red))
	}
	if f, red := ScanArgs(json.RawMessage(`{"to":["+94 77 123 4567"]} x`), nil, nil); f != nil || red != nil {
		t.Fatal("invalid JSON gives nothing", f, string(red))
	}
	// A long string is scanned at its start and its end only.
	mid := strings.Repeat("a", 40<<10) + " 4111 1111 1111 1111 " + strings.Repeat("a", 40<<10)
	if f, _ := ScanArgs(mustJSON(t, map[string]string{"text": mid}), nil, nil); len(f) != 0 {
		t.Fatal("the middle of a long string is not scanned:", f)
	}
	end := strings.Repeat("a", 70<<10) + " 4111 1111 1111 1111"
	if f, _ := ScanArgs(mustJSON(t, map[string]string{"text": end}), nil, nil); len(f) != 1 || f[0].Start != 70<<10+1 {
		t.Fatalf("%+v", f)
	}
}

func TestPartialExact(t *testing.T) {
	r := NewRedactor([]string{"pypi-AgEIcHlwaS5vcmc0123456789"})
	if got := r.PartialExact("upload with pypi-AgEIcH"); got != len("pypi-AgEIcH") {
		t.Fatal(got)
	}
	if got := r.PartialExact("no match here"); got != 0 {
		t.Fatal(got)
	}
	var nilR *Redactor
	if got := nilR.PartialExact("pypi-AgEIcH"); got != 0 {
		t.Fatal(got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
