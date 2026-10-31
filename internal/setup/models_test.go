package setup

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/provider"
)

// modelWizard returns a wizard that sends each provider call to the server at url.
func modelWizard(url string, lines ...string) (*Wizard, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &Wizard{In: answers(lines...), Out: out, HC: redirectClient(url)}, out
}

// deepseekAnswers are the answers of a new DeepSeek entry with a key.
var deepseekAnswers = providerAnswers{preset: "deepseek", key: "dk-1"}

func TestAskModelPrintsListAndReadsNumber(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"deepseek-flash", "deepseek-v4-pro"})
	w, out := modelWizard(prov.URL, "2")
	got, err := w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-v4-pro")
	want := "Models of DeepSeek:\n   1) deepseek-flash\n   2) deepseek-v4-pro\n" +
		"Type a number, a text to filter the list, or a model ID.\nModel: "
	if !strings.Contains(out.String(), want) {
		t.Fatalf("no %q in\n%s", want, out.String())
	}
	eq(t, prov.listCalls.Load(), int32(1))
	eq(t, prov.calls.Load(), int32(0))
}

func TestAskModelCurrentModelIsDefault(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"deepseek-flash", "deepseek-v4-pro"})
	w, out := modelWizard(prov.URL, "")
	a := deepseekAnswers
	a.model = "deepseek-v4-pro"
	got, err := w.askModel(ctx, "primary", a)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-v4-pro")
	if !strings.Contains(out.String(), "Model [deepseek-v4-pro]: ") {
		t.Fatal(out.String())
	}
}

func TestAskModelExactID(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"glm-5.3-flash", "glm-5.3"})
	w, out := modelWizard(prov.URL, "glm-5.3")
	got, err := w.askModel(ctx, "primary", providerAnswers{preset: "zai", key: "zk-1"})
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "glm-5.3")
	if !strings.Contains(out.String(), "Models of Z.AI:") || strings.Contains(out.String(), "No model matches") {
		t.Fatal(out.String())
	}
}

func TestAskModelRefusesBadAnswers(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"deepseek-flash", "deepseek-v4-pro"})
	w, out := modelWizard(prov.URL, "", "15", "0", "1")
	got, err := w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-flash")
	eq(t, strings.Count(out.String(), "A value is necessary."), 1)
	eq(t, strings.Count(out.String(), "Enter a number from 1 to 2."), 2)
	// A bad answer asks the question again, but it does not print the list again.
	eq(t, strings.Count(out.String(), "Models of DeepSeek:"), 1)
}

func TestAskModelNoMatchConfirm(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"deepseek-flash", "deepseek-v4-pro"})
	question := `No model matches "glm-6". Use "glm-6" as the model ID? [y/N]: `

	w, out := modelWizard(prov.URL, "glm-6", "y")
	got, err := w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "glm-6")
	if !strings.Contains(out.String(), question) {
		t.Fatal(out.String())
	}

	// No asks the model question again.
	w, out = modelWizard(prov.URL, "glm-6", "n", "1")
	got, err = w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-flash")
	eq(t, strings.Count(out.String(), "Model: "), 2)
}

func TestAskModelEndOfInput(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"deepseek-flash"})
	w, out := modelWizard(prov.URL)
	if _, err := w.askModel(ctx, "primary", deepseekAnswers); !errors.Is(err, errInputEnded) {
		t.Fatal(err, out.String())
	}
}

func TestAskModelCustomEntryShowsBaseURL(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"llama3"})
	out := &bytes.Buffer{}
	w := &Wizard{In: answers("1"), Out: out, HC: &http.Client{}}
	got, err := w.askModel(ctx, "primary", providerAnswers{format: "openai", baseURL: prov.URL + "/v1"})
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "llama3")
	if !strings.Contains(out.String(), "Models of "+prov.URL+"/v1:\n   1) llama3\n") {
		t.Fatal(out.String())
	}
}

func TestAskModelListFailure(t *testing.T) {
	down := downProvider(t)
	a := providerAnswers{format: "openai", baseURL: down.URL, key: "sk-1"}
	w, out := modelWizard(down.URL, "my-model")
	got, err := w.askModel(ctx, "primary", a)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "my-model")
	if !strings.Contains(out.String(), "The model list is not available: HTTP 503.\nModel: ") {
		t.Fatal(out.String())
	}

	// A second run: the current model is the default.
	a.model = "m"
	w, out = modelWizard(down.URL, "")
	if got, err = w.askModel(ctx, "primary", a); err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "m")
	if !strings.Contains(out.String(), "Model [m]: ") {
		t.Fatal(out.String())
	}
}

func TestPrintModelsTwoGroups(t *testing.T) {
	out := &bytes.Buffer{}
	w := &Wizard{Out: out}
	list := provider.ModelList{Groups: []provider.ModelGroup{
		{Title: "Most used this week", Models: []string{"a/one", "b/two"}},
		{Title: "Best for agents", Models: []string{"c/three"}},
	}, ToolsOnly: true, Search: true}
	shown := w.printModels("OpenRouter", list)
	if !slices.Equal(shown, []string{"a/one", "b/two", "c/three"}) {
		t.Fatal(shown)
	}
	want := "Models of OpenRouter. Most used this week, with tool support:\n   1) a/one\n   2) b/two\n" +
		"Best for agents:\n   3) c/three\nType a number, a text to search, or a model ID.\n"
	eq(t, out.String(), want)

	// A number selects a row of the second group.
	w = &Wizard{In: answers("3"), Out: &bytes.Buffer{}}
	pc := config.Provider{Preset: "openrouter", Format: "openai", BaseURL: "https://openrouter.ai/api/v1"}
	got, err := w.pickModel(ctx, "primary", pc, list, "")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, got, "c/three")
}

func TestAskModelCapsRows(t *testing.T) {
	var ids []string
	for i := 1; i <= 61; i++ {
		ids = append(ids, fmt.Sprintf("model-%02d", i))
	}
	prov := newFakeProviderWith(t, ids)
	more := "… and 41 more. Type a text to filter the list.\n"
	// 21 is not a printed number. MODEL- matches each ID, so the new list has the cap too.
	// An exact ID of the full list works also when it is not printed.
	w, out := modelWizard(prov.URL, "21", "MODEL-", "model-21")
	got, err := w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "model-21")
	want := "  20) model-20\n" + more + "Type a number, a text to filter the list, or a model ID.\n"
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "21) model-21") {
		t.Fatalf("no %q in\n%s", want, out.String())
	}
	eq(t, strings.Count(out.String(), more), 2)
	eq(t, strings.Count(out.String(), "Enter a number from 1 to 20."), 1)
}

func TestAskModelFilterPrintsNewList(t *testing.T) {
	prov := newFakeProviderWith(t, []string{"glm-5.3", "deepseek-flash", "deepseek-v4-pro"})
	// The numbers point to the new list: 1 is deepseek-flash, not glm-5.3.
	w, out := modelWizard(prov.URL, "DEEP", "1")
	got, err := w.askModel(ctx, "primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-flash")
	if !strings.Contains(out.String(), "Models of DeepSeek:\n   1) deepseek-flash\n   2) deepseek-v4-pro\n") {
		t.Fatal(out.String())
	}
	eq(t, strings.Count(out.String(), "Models of DeepSeek:"), 2)
	// A generic list filters in the wizard. It sends no search call.
	eq(t, prov.listCalls.Load(), int32(1))
	if strings.Contains(out.String(), "No model matches") {
		t.Fatal(out.String())
	}
}

// openRouterModels returns 15 IDs for the two OpenRouter groups and one ID that only the
// search finds.
func openRouterModels() []string {
	var ids []string
	for i := 1; i <= 15; i++ {
		ids = append(ids, fmt.Sprintf("lab/model-%02d", i))
	}
	return append(ids, "qwen/qwen3.8-max-0902")
}

func TestAskModelOpenRouterSearch(t *testing.T) {
	prov := newFakeProviderWith(t, openRouterModels())
	or := providerAnswers{preset: "openrouter", key: "or-1"}
	w, out := modelWizard(prov.URL, "qwen3.8", "1")
	got, err := w.askModel(ctx, "primary", or)
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "qwen/qwen3.8-max-0902")
	if !strings.Contains(out.String(), "   1) qwen/qwen3.8-max-0902\nType a number, a text to search, or a model ID.\n") {
		t.Fatal(out.String())
	}
	if !strings.Contains(prov.query(), "q=qwen3.8") {
		t.Fatal(prov.query())
	}
	// Two group calls, then one search call.
	eq(t, prov.listCalls.Load(), int32(3))

	// The full ID of a search result is an exact match. It sends no second search.
	prov = newFakeProviderWith(t, openRouterModels())
	w, out = modelWizard(prov.URL, "qwen3.8", "qwen/qwen3.8-max-0902")
	if got, err = w.askModel(ctx, "primary", or); err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "qwen/qwen3.8-max-0902")
	eq(t, prov.listCalls.Load(), int32(3))
}

func TestAskModelOpenRouterSearchNoMatch(t *testing.T) {
	prov := newFakeProviderWith(t, openRouterModels())
	w, out := modelWizard(prov.URL, "glm-6", "y")
	got, err := w.askModel(ctx, "primary", providerAnswers{preset: "openrouter", key: "or-1"})
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "glm-6")
	if !strings.Contains(out.String(), `No model matches "glm-6". Use "glm-6" as the model ID? [y/N]: `) {
		t.Fatal(out.String())
	}
}

func TestPickModelFiltersWhenNoSearch(t *testing.T) {
	// A DeepSeek entry has no search, so SearchModels gives ErrNoSearch and the wizard
	// filters the full list.
	down := downProvider(t)
	w, out := modelWizard(down.URL, "flash", "1")
	pc, err := w.resolvedEntry("primary", deepseekAnswers)
	if err != nil {
		t.Fatal(err)
	}
	list := provider.ModelList{Groups: []provider.ModelGroup{{Models: []string{"deepseek-v4-pro", "deepseek-flash"}}}, Search: true}
	got, err := w.pickModel(ctx, "primary", pc, list, "")
	if err != nil {
		t.Fatal(err, out.String())
	}
	eq(t, got, "deepseek-flash")
	if !strings.Contains(out.String(), "   1) deepseek-flash\n") {
		t.Fatal(out.String())
	}
}
