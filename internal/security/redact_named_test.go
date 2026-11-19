package security

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
)

func TestLabel(t *testing.T) {
	if got := Label("PYPI_TOKEN"); got != "«PYPI_TOKEN»" {
		t.Fatal(got)
	}
	if !IsLabelOrHandle(Label("GH_TOKEN")) {
		t.Fatal("a label must count as masked")
	}
}

func TestNamedRedactorLabels(t *testing.T) {
	const pypi = "pypi-AgEIcHlwaS5vcmc-0123456789"
	r := NewNamedRedactor(map[string]string{
		"PYPI_TOKEN":  pypi,
		"LONG":        "abcdefgh-long-secret-value",
		"SHORTER":     "abcdefgh",
		"PIN":         "1234567",
		"DB_PASSWORD": `pa"ss\word-123456`,
		"A_KEY":       "shared-value-0123456789",
		"B_KEY":       "shared-value-0123456789",
	})
	for _, tc := range []struct{ in, want string }{
		// A read of .env shows the key, not the value.
		{"PYPI_TOKEN=" + pypi, "PYPI_TOKEN=«PYPI_TOKEN»"},
		{"TWINE_PASSWORD=" + pypi, "TWINE_PASSWORD=«PYPI_TOKEN»"},
		// The longest value goes first.
		{"abcdefgh-long-secret-value and abcdefgh", "«LONG» and «SHORTER»"},
		// A value under 8 characters still shows.
		{"pin 1234567 stays", "pin 1234567 stays"},
		// The JSON form and the raw form get the same label.
		{`{"x":"pa\"ss\\word-123456"}`, `{"x":"«DB_PASSWORD»"}`},
		{`raw pa"ss\word-123456`, "raw «DB_PASSWORD»"},
		// The first name in sorted order wins for a shared value.
		{"shared-value-0123456789", "«A_KEY»"},
	} {
		if got := r.Redact(tc.in); got != tc.want {
			t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if got := r.RedactExact(tc.in); got != tc.want {
			t.Errorf("RedactExact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	in := "x " + pypi + ` y {"x":"pa\"ss\\word-123456"}`
	want := []Match{at(t, in, pypi, "exact", "PYPI_TOKEN"), at(t, in, `pa\"ss\\word-123456`, "exact", "DB_PASSWORD")}
	if got := r.Find(in); !slices.Equal(got, want) {
		t.Fatalf("Find = %+v, want %+v", got, want)
	}
}

// NewRedactor keeps the Mask form.
func TestUnnamedRedactorKeepsMask(t *testing.T) {
	const v = "unnamed-value-0123456789"
	if got := NewRedactor([]string{v}).Redact("x " + v); got != "x "+Mask(v) {
		t.Fatal(got)
	}
}

func TestRedactorAdd(t *testing.T) {
	r := NewNamedRedactor(map[string]string{"GH_TOKEN": "ghp-old-value-0123456789"})
	r.Add("GH_TOKEN", "ghp-new-value-0123456789")
	r.Add("PYPI_TOKEN", "short")
	got := r.Redact("old ghp-old-value-0123456789 new ghp-new-value-0123456789 short")
	if want := "old «GH_TOKEN» new «GH_TOKEN» short"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// A NewRedactor value keeps its Mask form, and an added value gets a label.
	m := NewRedactor([]string{"unnamed-value-0123456789"})
	m.Add("NEW_KEY", "added-value-0123456789")
	if got, want := m.Redact("unnamed-value-0123456789 added-value-0123456789"),
		Mask("unnamed-value-0123456789")+" «NEW_KEY»"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	var none *Redactor
	none.Add("X_TOKEN", "value-0123456789") // no panic
}

// Run with -race: a reader never sees a replacer that changes.
func TestRedactorAddConcurrent(t *testing.T) {
	r := NewNamedRedactor(nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.Add(fmt.Sprintf("K%d", i), fmt.Sprintf("value-%d-0123456789", i))
		}()
		go func() {
			defer wg.Done()
			_ = r.Redact("value-3-0123456789")
			_ = r.Find("value-3-0123456789")
		}()
	}
	wg.Wait()
	for i := range 8 {
		if got, want := r.Redact(fmt.Sprintf("value-%d-0123456789", i)), Label(fmt.Sprintf("K%d", i)); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestKnownSecretPairs(t *testing.T) {
	got := KnownSecretPairs(
		map[string]string{"PYPI_TOKEN": "pypi-dotenv-0123456789", "EMPTY": "", "HELIUS": "helius-value-01"},
		[]string{"GH_TOKEN=env-gh-0123456789", "PATH=/usr/bin", "PYPI_TOKEN=pypi-process-0123456789", "NOEQ", "X_SECRET="})
	want := map[string]string{
		"PYPI_TOKEN": "pypi-dotenv-0123456789",
		"HELIUS":     "helius-value-01",
		"GH_TOKEN":   "env-gh-0123456789",
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
