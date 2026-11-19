package security

import (
	"slices"
	"strings"
	"testing"
)

// at returns the Match of the first sub in s.
func at(t *testing.T, s, sub, kind, name string) Match {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("%q is not in %q", sub, s)
	}
	return Match{Start: i, End: i + len(sub), Kind: kind, Name: name}
}

func TestRedactorFind(t *testing.T) {
	const exact = "my-helius-value-0001"
	const key = "sk-proj-ABCDEFGHIJKLMNOPQRSTUV"
	const tg = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	pem := "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234 " + key + "\nabcd\n-----END RSA PRIVATE KEY-----"
	r := NewRedactor([]string{exact})
	type span struct{ sub, kind string }
	for _, tc := range []struct {
		name, in string
		want     []span
	}{
		{"exact", "using " + exact + " now", []span{{exact, "exact"}}},
		{"prefix", "key " + key + " end", []span{{key, "prefix"}}},
		{"exact wins over a pattern", "HELIUS_TOKEN=" + exact, []span{{exact, "exact"}}},
		{"prefix wins over env assign", "OPENAI_API_KEY=" + key, []span{{key, "prefix"}}},
		{"env assign", "X_TOKEN=abc123def456ghi", []span{{"abc123def456ghi", "pattern"}}},
		{"env assign quoted", `X_TOKEN="abc123def456"`, []span{{"abc123def456", "pattern"}}},
		{"json field", `{"token": "abc123def456"}`, []span{{"abc123def456", "pattern"}}},
		{"auth header", "Authorization: Bearer abc123def456ghi", []span{{"abc123def456ghi", "pattern"}}},
		{"basic header", "Authorization: Basic YWxpY2U6aHVudGVyMg==", []span{{"YWxpY2U6aHVudGVyMg==", "pattern"}}},
		{"url userinfo", "git clone https://alice:hunter2pw@github.com/x", []span{{"hunter2pw", "pattern"}}},
		{"db connstr", "postgres://admin:dbpass@db.internal:5432/app", []span{{"dbpass", "pattern"}}},
		{"flag", "gh api --token abc123tok /user", []span{{"abc123tok", "pattern"}}},
		{"flag quoted", `tool --password "two words" next`, []span{{"two words", "pattern"}}},
		{"telegram", "bot123456789:" + tg, []span{{tg, "pattern"}}},
		{"query", "GET https://x.io/a?access_token=abcdef123456&q=ok", []span{{"abcdef123456", "pattern"}}},
		{"private key holds a prefix key", "x\n" + pem + "\ny", []span{{pem, "pattern"}}},
		{"two in order", "key " + key + " and X_TOKEN=abc123def456ghi", []span{{key, "prefix"}, {"abc123def456ghi", "pattern"}}},
		{"none", "ls -la /tmp", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var want []Match
			for _, w := range tc.want {
				want = append(want, at(t, tc.in, w.sub, w.kind, ""))
			}
			got := r.Find(tc.in)
			if !slices.Equal(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			// Redact masks each span that Find reports.
			out := r.Redact(tc.in)
			for _, m := range got {
				if strings.Contains(out, tc.in[m.Start:m.End]) {
					t.Fatalf("Redact keeps %q: %q", tc.in[m.Start:m.End], out)
				}
			}
		})
	}
}

// A label and a handle stay as they are in each key/value form, and Find does not report them.
func TestFindSkipsLabelsAndHandles(t *testing.T) {
	r := NewRedactor(nil)
	for _, in := range []string{
		"X_TOKEN=$TMP_SECRET_7",
		`X_TOKEN="${TMP_SECRET_7}"`,
		"PYPI_TOKEN=«PYPI_TOKEN»",
		`{"token": "$TMP_SECRET_7"}`,
		`{"api_key": "«OPENAI_API_KEY»"}`,
		"Authorization: Bearer $TMP_SECRET_7",
		"Authorization: Bearer «GH_TOKEN»",
		"curl https://u:$TMP_SECRET_7@h/x",
		"curl https://u:«GH_TOKEN»@h/x",
		"gh api --token $TMP_SECRET_7 /user",
		"gh api --token=«GH_TOKEN» /user",
		`gh api --token "${TMP_SECRET_12}" /user`,
		"GET https://x.io/a?token=$TMP_SECRET_7&q=ok",
	} {
		if got := r.Find(in); len(got) != 0 {
			t.Errorf("%q: Find gives %+v", in, got)
		}
		if got := r.Redact(in); got != in {
			t.Errorf("%q: Redact gives %q", in, got)
		}
	}
	// A real value next to a handle is still found.
	in := "X_TOKEN=$TMP_SECRET_7 Y_TOKEN=abc123def456ghi"
	if got, want := r.Find(in), []Match{at(t, in, "abc123def456ghi", "pattern", "")}; !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestIsLabelOrHandle(t *testing.T) {
	for _, v := range []string{"$TMP_SECRET_1", "$TMP_SECRET_64", "${TMP_SECRET_12}", "«PYPI_TOKEN»", "«a_b»", "«_X1»"} {
		if !IsLabelOrHandle(v) {
			t.Errorf("%q must be a label or a handle", v)
		}
	}
	for _, v := range []string{"", "***", "$TMP_SECRET_", "$TMP_SECRET_x", "$TMP_SECRET_1x", "${TMP_SECRET_1",
		"TMP_SECRET_1", `"$TMP_SECRET_1"`, "«»", "«PYPI TOKEN»", "«1ABC»", "«PYPI_TOKEN»x", "x«PYPI_TOKEN»"} {
		if IsLabelOrHandle(v) {
			t.Errorf("%q must not be a label or a handle", v)
		}
	}
}
