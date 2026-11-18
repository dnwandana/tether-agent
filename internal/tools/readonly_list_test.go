package tools_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/setup"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// readOnlyMixed holds the tools with read and write actions. Each one has a ReadOnlyCall method.
var readOnlyMixed = []string{"browser_cookies", "cronjob", "memory"}

// TestEachToolIsInOneReadOnlyList: a new built-in tool fails this test until it is in the
// allow list, the deny list, or the list of mixed tools.
func TestEachToolIsInOneReadOnlyList(t *testing.T) {
	t.Cleanup(func() {
		security.AddProtectedPathPatterns(nil)
		security.SetHumanOnlyRules(nil)
	})
	r, done, err := setup.ToolsDocRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	denied := tools.ReadOnlyDenied()
	for _, tl := range r.All() {
		name := tl.Name()
		in := 0
		if slices.Contains(denied, name) {
			in++
		}
		if slices.Contains(readOnlyMixed, name) {
			in++
		} else if tools.ReadOnlyAllowed(tl, json.RawMessage(`{}`)) {
			in++
		}
		if in != 1 {
			t.Errorf("%s is in %d read-only lists; put it in exactly one list in internal/tools/readonly.go", name, in)
		}
	}
}

// TestMixedToolsHaveReadOnlyCall: the mixed tools, and only they, decide each call.
func TestMixedToolsHaveReadOnlyCall(t *testing.T) {
	t.Cleanup(func() {
		security.AddProtectedPathPatterns(nil)
		security.SetHumanOnlyRules(nil)
	})
	r, done, err := setup.ToolsDocRegistry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	for _, tl := range r.All() {
		_, has := tl.(tools.ReadOnlyCaller)
		if want := slices.Contains(readOnlyMixed, tl.Name()); has != want {
			t.Errorf("%s: ReadOnlyCaller is %v, want %v", tl.Name(), has, want)
		}
	}
}
