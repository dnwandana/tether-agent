package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/app"
	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/db"
	"github.com/dnwandana/tether-agent/internal/security"
	"github.com/dnwandana/tether-agent/internal/telegram"
)

// defaultTelegramWait is the maximum time that the wizard waits for the /start of the owner.
const defaultTelegramWait = 5 * time.Minute

// maxPollS is the long-poll time of one getUpdates call of the wizard.
const maxPollS = 25

// errInputEnded is the error when the input ends before a required answer.
var errInputEnded = errors.New("setup: the input ended before the setup was complete; run tether setup again")

// envRef matches a config value that is only a ${NAME} reference.
var envRef = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Wizard is the interactive setup of Tether Agent. It is the only code that writes
// config.yaml. A secret goes to .env, and the config refers to it with ${NAME}.
type Wizard struct {
	In      *bufio.Reader
	Out     io.Writer
	DataDir string
	// HC is the client of the provider calls and the Bot API calls. Nil uses a new client.
	HC *http.Client
	// TelegramBase replaces the Bot API root. Empty uses the real Bot API.
	TelegramBase string
	// Now returns the current time for the pairing code. Nil uses time.Now.
	Now func() time.Time
	// LookPath finds the host programs of step 4. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// TelegramWait is the maximum wait for the /start of the owner. Zero uses 5 minutes.
	TelegramWait time.Duration
	// BinPath is the tether binary for the systemd unit of step 5.
	BinPath string
	// System writes a system unit instead of a user unit in step 5.
	System bool
	// GOOS replaces runtime.GOOS. Step 5 runs only on linux.
	GOOS string
	// Term is the file of In. When Term is a terminal, the echo is off while the user types
	// an API key or a bot token. Nil shows every answer.
	Term *os.File

	// term replaces the terminal access in tests. Nil uses the real terminal.
	term   *termOps
	cfg    *configDoc
	env    *envFile
	dotenv map[string]string
	// saved holds the files that this run wrote once already. Only the first write of a
	// run makes the .bak copy, so the copy keeps the state from before the run.
	saved map[string]bool
	eof   bool
}

func (w *Wizard) configPath() string { return filepath.Join(w.DataDir, "config.yaml") }
func (w *Wizard) envPath() string    { return filepath.Join(w.DataDir, ".env") }

func (w *Wizard) hc() *http.Client {
	if w.HC != nil {
		return w.HC
	}
	return &http.Client{}
}

func (w *Wizard) say(format string, args ...any) { fmt.Fprintf(w.Out, format+"\n", args...) }

// Run runs the setup steps: the provider, the fallback list, Telegram, the host
// programs, and the systemd unit. It saves config.yaml and .env after each step that
// changes them.
func (w *Wizard) Run(ctx context.Context) error {
	if err := os.MkdirAll(w.DataDir, 0o700); err != nil {
		return fmt.Errorf("setup: cannot make the data folder %s: %w", w.DataDir, err)
	}
	var err error
	if w.cfg, err = readConfigDoc(w.configPath()); err != nil {
		return err
	}
	if w.env, err = readEnvFile(w.envPath()); err != nil {
		return err
	}
	if w.dotenv, err = security.LoadDotEnv(w.envPath()); err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	w.saved = map[string]bool{}
	w.say("tether setup. The data folder is %s.", w.DataDir)
	w.say("Press Enter to keep the value in [brackets].")

	w.say("\nStep 1 of 5: the primary provider")
	if err := w.providerStep(ctx, "primary", "PRIMARY_API_KEY", nil); err != nil {
		return err
	}

	w.say("\nStep 2 of 5: the fallback providers (optional)")
	if err := w.fallbackStep(ctx); err != nil {
		return err
	}

	w.say("\nStep 3 of 5: Telegram (optional)")
	q := "Set up Telegram?"
	if w.cfg.get("telegram", "enabled") == "true" {
		q = "Change the Telegram setup?"
	}
	if yes, err := w.confirm(q); err != nil {
		return err
	} else if yes {
		if err := w.telegramStep(ctx); err != nil {
			return err
		}
	}

	w.say("\nStep 4 of 5: the host programs")
	w.extrasStep()

	w.say("\nStep 5 of 5: the systemd service (optional)")
	if err := w.serviceStep(); err != nil {
		return err
	}
	w.say("\nThe setup is complete. Run tether doctor to check everything.")
	return nil
}

// ask prints the question with the default value and returns the answer. An empty answer
// gives def. After the end of the input, every question gives its default.
func (w *Wizard) ask(question, def string) string {
	if def != "" {
		fmt.Fprintf(w.Out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(w.Out, "%s: ", question)
	}
	return w.readAnswer(def)
}

// readAnswer reads one answer line. An empty answer gives def. After the end of the
// input, it gives def at once.
func (w *Wizard) readAnswer(def string) string {
	if w.eof {
		fmt.Fprintln(w.Out)
		return def
	}
	line, err := w.In.ReadString('\n')
	if err != nil {
		w.eof = true
		if line == "" {
			fmt.Fprintln(w.Out)
		}
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// askRequired is ask for a value that must not be empty. It asks again after an empty
// answer, and fails when the input ends.
func (w *Wizard) askRequired(question, def string) (string, error) {
	for {
		v := w.ask(question, def)
		if v != "" {
			return v, nil
		}
		if w.eof {
			return "", errInputEnded
		}
		w.say("A value is necessary.")
	}
}

// confirm asks a [y/N] question. Only "y" or "yes" is a yes.
func (w *Wizard) confirm(question string) (bool, error) {
	v := strings.ToLower(w.ask(question+" [y/N]", ""))
	return v == "y" || v == "yes", nil
}

// lookup returns the value of a variable from the process environment or from .env.
func (w *Wizard) lookup(name string) (string, bool) {
	return security.EnvLookup(w.dotenv)(name)
}

// providerAnswers holds the answers of the provider questions.
type providerAnswers struct {
	// preset is the chosen preset id. It is "" for a custom entry.
	preset                      string
	format, baseURL, key, model string
	// keyRef is the variable of the key in the config. It is "" when the entry has no
	// api_key and no answer gave one.
	keyRef string
	// keyFromUser is true when key must go to .env under keyRef.
	keyFromUser bool
}

// providerStep asks for one provider, tests it, and saves it. A failed test call asks the
// questions one more time. A second failure stops the wizard. keyVar is the variable of a
// new key for a custom entry. A non-nil chosen is the menu answer of the first try.
func (w *Wizard) providerStep(ctx context.Context, name, keyVar string, chosen *string) error {
	a := w.currentProvider(name)
	for try := 1; ; try++ {
		var err error
		if try == 1 && chosen != nil {
			a, err = w.askChosen(ctx, name, keyVar, *chosen, a)
		} else {
			a, err = w.askProvider(ctx, name, keyVar, a)
		}
		if err != nil {
			return err
		}
		model, ms, err := w.testProvider(ctx, name, a)
		if err == nil {
			w.say("The test call works: model %s answered in %d ms.", model, ms)
			break
		}
		msg := w.redactor(a.key).Redact(err.Error())
		w.say("The test call failed: %s", msg)
		if try == 2 {
			return fmt.Errorf("setup: the test call to provider %s failed two times: %s. "+
				"Check the base URL, the API key, and the model, then run tether setup again", name, msg)
		}
		w.say("Check the values and try again.")
	}
	if a.keyFromUser {
		w.env.set(a.keyRef, a.key)
		w.dotenv[a.keyRef] = a.key
	}
	if a.preset != "" {
		pre, _ := config.PresetByID(a.preset)
		w.cfg.setString(a.preset, "providers", name, "preset")
		// A stale format or base_url wins over the preset, so the entry loses them.
		w.cfg.delete("providers", name, "format")
		w.cfg.delete("providers", name, "base_url")
		if a.keyRef == pre.KeyEnv {
			w.cfg.delete("providers", name, "api_key")
		} else {
			w.cfg.setString("${"+a.keyRef+"}", "providers", name, "api_key")
		}
	} else {
		w.cfg.delete("providers", name, "preset")
		w.cfg.setString(a.format, "providers", name, "format")
		w.cfg.setString(a.baseURL, "providers", name, "base_url")
		if a.keyRef == "" {
			w.cfg.delete("providers", name, "api_key")
		} else {
			w.cfg.setString("${"+a.keyRef+"}", "providers", name, "api_key")
		}
	}
	w.cfg.setString(a.model, "providers", name, "model")
	return w.save()
}

// testProvider resolves the preset of the answers and sends the test call. The key of
// the answers is the resolved key, so the resolution gets no env lookup.
func (w *Wizard) testProvider(ctx context.Context, name string, a providerAnswers) (string, int64, error) {
	pc, err := w.resolvedEntry(name, a)
	if err != nil {
		return "", 0, err
	}
	if pc.Unusable != "" {
		return "", 0, errors.New(pc.Unusable)
	}
	return TestProvider(ctx, name, pc, w.hc())
}

// currentProvider returns the current values of a provider entry as the defaults.
func (w *Wizard) currentProvider(name string) providerAnswers {
	a := providerAnswers{
		preset:  w.cfg.get("providers", name, "preset"),
		format:  w.cfg.get("providers", name, "format"),
		baseURL: w.cfg.get("providers", name, "base_url"),
		model:   w.cfg.get("providers", name, "model"),
	}
	raw := w.cfg.get("providers", name, "api_key")
	if m := envRef.FindStringSubmatch(raw); m != nil {
		a.keyRef = m[1]
		a.key, _ = w.lookup(m[1])
	} else if raw != "" {
		// A key in config.yaml moves to .env, so that the secrets stay in the 0600 file.
		a.key, a.keyFromUser = raw, true
	}
	return a
}

// presetGroup is one item of the preset menu. A group with two rows asks a second
// question.
type presetGroup struct {
	label string
	rows  []config.Preset
}

// presetGroups returns the items of the preset menu in wizard order.
func presetGroups() []presetGroup {
	var out []presetGroup
	for _, p := range config.Presets() {
		if n := len(out); n > 0 && out[n-1].label == p.Group {
			out[n-1].rows = append(out[n-1].rows, p)
			continue
		}
		out = append(out, presetGroup{label: p.Group, rows: []config.Preset{p}})
	}
	return out
}

// secondQuestions are the second questions of the groups with two rows.
var secondQuestions = map[string]string{"Z.AI": "Plan"}

// askPreset shows the preset menu and returns the chosen preset id, or "" for Custom. The
// default is the preset of a, else Custom for a custom entry, else the first item.
func (w *Wizard) askPreset(a providerAnswers) (string, error) {
	groups := presetGroups()
	custom := len(groups) + 1
	def, defRow := 1, 1
	switch {
	case a.preset != "":
		for gi, g := range groups {
			for ri, r := range g.rows {
				if r.ID == a.preset {
					def, defRow = gi+1, ri+1
				}
			}
		}
	case a.format != "":
		def = custom
	}
	for i, g := range groups {
		w.say("%4d) %s", i+1, g.label)
	}
	w.say("%4d) Custom", custom)
	n, err := w.askNumber("Provider", def, custom)
	if err != nil || n == custom {
		return "", err
	}
	g := groups[n-1]
	if len(g.rows) == 1 {
		return g.rows[0].ID, nil
	}
	if n != def {
		defRow = 1
	}
	for i, r := range g.rows {
		w.say("%4d) %s", i+1, r.Choice)
	}
	q := secondQuestions[g.label]
	if q == "" {
		q = "Choice"
	}
	r, err := w.askNumber(q, defRow, len(g.rows))
	if err != nil {
		return "", err
	}
	return g.rows[r-1].ID, nil
}

// askNumber asks for a number from 1 to max. It asks again after a bad answer.
func (w *Wizard) askNumber(question string, def, max int) (int, error) {
	for {
		v := w.ask(question, strconv.Itoa(def))
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= max {
			return n, nil
		}
		if w.eof {
			return 0, errInputEnded
		}
		w.say("Enter a number from 1 to %d.", max)
	}
}

// askProvider shows the preset menu, then asks the questions of the chosen item with a as
// the defaults.
func (w *Wizard) askProvider(ctx context.Context, name, keyVar string, a providerAnswers) (providerAnswers, error) {
	id, err := w.askPreset(a)
	if err != nil {
		return a, err
	}
	return w.askChosen(ctx, name, keyVar, id, a)
}

// askChosen asks the questions of the menu answer id. An empty id is Custom.
func (w *Wizard) askChosen(ctx context.Context, name, keyVar, id string, a providerAnswers) (providerAnswers, error) {
	if id == "" {
		return w.askCustom(ctx, name, keyVar, a)
	}
	return w.askPresetEntry(ctx, name, id, a)
}

// fallbackStep shows the fallback list and builds a new list when the user wants a
// change. A new list starts empty, and each added entry goes to the end. An old entry
// that the user does not add again stays in providers, but it is not in the list.
func (w *Wizard) fallbackStep(ctx context.Context) error {
	w.say("The fallback list is: %s.", listText(w.fallbackList()))
	if yes, err := w.confirm("Change the fallback list?"); err != nil || !yes {
		return err
	}
	list := []string{}
	used := map[string]bool{}
	for {
		yes, err := w.confirm("Add a fallback provider?")
		if err != nil {
			return err
		}
		if !yes {
			break
		}
		id, err := w.askPreset(providerAnswers{})
		if err != nil {
			return err
		}
		name, err := w.askEntryName(id, used)
		if err != nil {
			return err
		}
		if err := w.providerStep(ctx, name, entryKeyVar(name), &id); err != nil {
			return err
		}
		list = append(list, name)
		used[name] = true
		// Save the list after each entry, so that a stop later keeps the entries that work.
		w.cfg.setList(list, "agent", "fallback")
		if err := w.save(); err != nil {
			return err
		}
	}
	w.cfg.setList(list, "agent", "fallback")
	if err := w.save(); err != nil {
		return err
	}
	w.say("The fallback list is: %s.", listText(list))
	return nil
}

// fallbackList returns the fallback list of config.yaml with the rule of
// config.FallbackList: agent.fallback wins, else an entry "fallback" is the list.
func (w *Wizard) fallbackList() []string {
	if w.cfg.has("agent", "fallback") {
		return w.cfg.getList("agent", "fallback")
	}
	if w.cfg.has("providers", "fallback") {
		return []string{"fallback"}
	}
	return nil
}

// listText returns the names with commas, or "none".
func listText(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// entryKeyVar returns the variable of a new key for the custom entry name, for example
// FALLBACK_API_KEY.
func entryKeyVar(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
}

// entryNamePattern is the set of permitted entry names. A name also goes into the name of
// a variable in .env.
var entryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validEntryName returns an error when name cannot be a fallback entry. used holds the
// names that are in the new list already.
func validEntryName(name string, used map[string]bool) error {
	switch {
	case name == "primary":
		return errors.New("the name primary is for the primary provider")
	case !entryNamePattern.MatchString(name):
		return errors.New(`a name can contain only letters, digits, "-", and "_"`)
	case used[name]:
		return fmt.Errorf("the entry %s is in the fallback list already", name)
	}
	return nil
}

// askEntryName asks for the name of a fallback entry. The default is the preset id, or
// "custom". When an entry has that name or that preset already, the default gets a number,
// for example openrouter-2. The name of an existing entry changes that entry.
func (w *Wizard) askEntryName(id string, used map[string]bool) (string, error) {
	base := id
	if base == "" {
		base = "custom"
	}
	taken := func(n string) bool { return n == "primary" || used[n] || w.cfg.has("providers", n) }
	numbered := taken(base)
	for _, n := range w.cfg.keys("providers") {
		if id != "" && w.cfg.get("providers", n, "preset") == id {
			numbered = true
		}
	}
	def := base
	for i := 2; numbered; i++ {
		def = base + "-" + strconv.Itoa(i)
		numbered = taken(def)
	}
	for {
		v := w.ask("Name of the entry", def)
		err := validEntryName(v, used)
		if err == nil {
			if w.cfg.has("providers", v) {
				w.say("The entry %s exists. The answers change it.", v)
			}
			return v, nil
		}
		if w.eof {
			return "", errInputEnded
		}
		msg := err.Error()
		w.say("%s%s. Enter another name.", strings.ToUpper(msg[:1]), msg[1:])
	}
}

// askPresetEntry asks the key and the model of a preset entry.
func (w *Wizard) askPresetEntry(ctx context.Context, name, id string, a providerAnswers) (providerAnswers, error) {
	pre, _ := config.PresetByID(id)
	changed := a.preset != id
	if a.preset != "" && changed {
		// The model of another preset is not a good default.
		a.model = ""
	}
	a.preset, a.format, a.baseURL = id, pre.Format, pre.BaseURL
	if changed {
		// The old key belongs to another endpoint. The default is the key env of the
		// new preset.
		a.keyRef, a.keyFromUser = pre.KeyEnv, false
		a.key, _ = w.lookup(pre.KeyEnv)
	}
	if a.keyRef == "" {
		// An entry with no api_key takes its key from the key env of the preset. A
		// literal key from config.yaml goes to that variable.
		a.keyRef = pre.KeyEnv
		if !a.keyFromUser {
			a.key, _ = w.lookup(pre.KeyEnv)
		}
	}
	var err error
	if a, err = w.askKey(ctx, "API key ("+pre.KeyEnv+")", a); err != nil {
		return a, err
	}
	if a.keyFromUser && a.keyRef == pre.KeyEnv {
		a.keyRef = w.keyVarFor(name, pre.KeyEnv, a.key)
	}
	if a.model, err = w.askModel(ctx, name, a); err != nil {
		return a, err
	}
	return a, nil
}

// askCustom asks the four questions of a custom entry with a as the defaults.
func (w *Wizard) askCustom(ctx context.Context, name, keyVar string, a providerAnswers) (providerAnswers, error) {
	if a.preset != "" {
		// Start from the values of the old preset. The key of the preset does not go to
		// another host, so the key default is the variable of the entry.
		pre, _ := config.PresetByID(a.preset)
		a.format, a.baseURL = pre.Format, pre.BaseURL
		a.preset = ""
		a.keyRef, a.keyFromUser = keyVar, false
		a.key, _ = w.lookup(keyVar)
	}
	if a.keyRef == "" {
		a.keyRef = keyVar
	}
	def := a.format
	if def == "" {
		def = "openai"
	}
	for {
		a.format = strings.ToLower(w.ask("Format (openai, anthropic, or responses)", def))
		if a.format == "openai" || a.format == "anthropic" || a.format == "responses" {
			break
		}
		if w.eof {
			return a, errInputEnded
		}
		w.say("The format must be openai, anthropic, or responses.")
	}
	defURL := a.baseURL
	if defURL == "" {
		defURL = "https://api.openai.com/v1"
		if a.format == "anthropic" {
			defURL = "https://api.anthropic.com"
		}
	}
	var err error
	if a.baseURL, err = w.askRequired("Base URL", defURL); err != nil {
		return a, err
	}
	if a, err = w.askKey(ctx, "API key (or ${NAME} for a variable in .env)", a); err != nil {
		return a, err
	}
	if _, set := w.lookup(keyVar); a.key == "" && a.keyRef == keyVar && !set {
		// An entry with no key, for example a local model server, gets no api_key line. A
		// reference to a variable that is not set stops the load.
		a.keyRef = ""
	}
	if a.model, err = w.askModel(ctx, name, a); err != nil {
		return a, err
	}
	return a, nil
}

// keyVarFor returns the variable in .env for the key of a preset entry. It is keyEnv when
// .env has no value for keyEnv, when the value is key, or when no other entry uses
// keyEnv. Else it is the first free <keyEnv>_<n> from 2, so a second entry does not
// replace the key of the first entry.
func (w *Wizard) keyVarFor(entry, keyEnv, key string) string {
	if cur := w.dotenv[keyEnv]; cur == "" || cur == key || !w.otherEntryUses(entry, keyEnv) {
		return keyEnv
	}
	for n := 2; ; n++ {
		v := keyEnv + "_" + strconv.Itoa(n)
		if cur := w.dotenv[v]; cur == "" || cur == key {
			return v
		}
	}
}

// otherEntryUses reports whether a provider entry other than entry takes its key from the
// variable v: through api_key: ${v}, or through the key env of its preset.
func (w *Wizard) otherEntryUses(entry, v string) bool {
	for _, name := range w.cfg.keys("providers") {
		if name == entry {
			continue
		}
		raw := w.cfg.get("providers", name, "api_key")
		if m := envRef.FindStringSubmatch(raw); m != nil {
			if m[1] == v {
				return true
			}
			continue
		}
		if raw != "" {
			continue
		}
		pre, ok := config.PresetByID(w.cfg.get("providers", name, "preset"))
		base := strings.TrimRight(w.cfg.get("providers", name, "base_url"), "/")
		if ok && pre.KeyEnv == v && (base == "" || base == strings.TrimRight(pre.BaseURL, "/")) {
			return true
		}
	}
	return false
}

// askKey asks for the API key. Enter keeps the current key, and a ${NAME} answer refers
// to a variable in .env.
func (w *Wizard) askKey(ctx context.Context, question string, a providerAnswers) (providerAnswers, error) {
	if a.key != "" {
		question += " [Enter keeps the current key]"
	}
	v, err := w.askSecret(ctx, question)
	if err != nil {
		return a, err
	}
	if v != "" {
		if m := envRef.FindStringSubmatch(v); m != nil {
			val, ok := w.lookup(m[1])
			if !ok {
				w.say("Note: %s is not set in .env or the environment.", m[1])
			}
			a.key, a.keyRef, a.keyFromUser = val, m[1], false
		} else {
			a.key, a.keyFromUser = v, true
		}
	} else if a.key == "" {
		w.say("Note: no API key is set.")
	}
	return a, nil
}

// redactor masks the .env values and the extra values in the text for the terminal.
func (w *Wizard) redactor(extra ...string) *security.Redactor {
	vals := map[string]string{}
	for k, v := range w.dotenv {
		vals[k] = v
	}
	for i, v := range extra {
		vals["SETUP_EXTRA_"+strconv.Itoa(i)] = v
	}
	return app.NewRedactor(vals)
}

// save writes config.yaml and .env with mode 0600. The first write of each file in a run
// copies the old file to a .bak file.
func (w *Wizard) save() error {
	cfgData, err := w.cfg.bytes()
	if err != nil {
		return fmt.Errorf("setup: cannot encode the config: %w", err)
	}
	files := []struct {
		path string
		data []byte
	}{{w.envPath(), w.env.bytes()}, {w.configPath(), cfgData}}
	for _, f := range files {
		if f.data == nil {
			continue
		}
		if err := writeSecretFile(f.path, f.data, !w.saved[f.path]); err != nil {
			return err
		}
		w.saved[f.path] = true
	}
	return nil
}

// telegramStep checks the bot token, waits for the /start of the owner, and approves the
// owner.
func (w *Wizard) telegramStep(ctx context.Context) error {
	keyRef := "TELEGRAM_BOT_TOKEN"
	raw := w.cfg.get("telegram", "bot_token")
	cur, _ := w.lookup(keyRef)
	if m := envRef.FindStringSubmatch(raw); m != nil {
		keyRef = m[1]
		cur, _ = w.lookup(keyRef)
	} else if raw != "" {
		cur = raw
	}
	var (
		token string
		me    telegram.User
	)
	for try := 1; ; try++ {
		q := "Bot token from @BotFather"
		if cur != "" {
			q += " [Enter keeps the current token]"
		}
		var err error
		if token, err = w.askSecret(ctx, q); err != nil {
			return err
		}
		if token == "" {
			token = cur
		}
		if token == "" {
			return errInputEnded
		}
		cctx, cancel := context.WithTimeout(ctx, checkTimeout)
		me, err = w.bot(token).GetMe(cctx)
		cancel()
		if err == nil {
			break
		}
		msg := w.redactor(token).Redact(err.Error())
		w.say("The token check (getMe) failed: %s", msg)
		if try == 2 {
			return fmt.Errorf("setup: the bot token check failed two times: %s. Get the token from @BotFather, then run tether setup again", msg)
		}
		cur = ""
	}
	w.say("The bot is @%s.", me.Username)
	w.env.set(keyRef, token)
	w.dotenv[keyRef] = token
	w.cfg.setString("${"+keyRef+"}", "telegram", "bot_token")
	if err := w.save(); err != nil {
		return err
	}

	w.say("Send /start to @%s now. The wait is at most %s.", me.Username, w.telegramWait())
	from, err := w.waitStart(ctx, token)
	if err != nil {
		return err
	}
	if from == nil {
		w.say("No /start received; run tether setup again later.")
		return nil
	}
	return w.approveOwner(ctx, from)
}

func (w *Wizard) bot(token string) *telegram.Client {
	return &telegram.Client{Token: token, BaseURL: w.TelegramBase, HC: w.hc()}
}

func (w *Wizard) telegramWait() time.Duration {
	if w.TelegramWait > 0 {
		return w.TelegramWait
	}
	return defaultTelegramWait
}

// waitStart long-polls until a /start arrives in a private chat. It returns the sender, or
// nil when the wait ends without a /start.
func (w *Wizard) waitStart(ctx context.Context, token string) (*telegram.User, error) {
	ctx, cancel := context.WithTimeout(ctx, w.telegramWait())
	defer cancel()
	c := w.bot(token)
	var offset int64
	for {
		left := time.Until(deadline(ctx))
		pollS := min(maxPollS, int(left/time.Second))
		ups, err := c.GetUpdates(ctx, offset, pollS)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
				return nil, errors.New("setup: another process gets the updates of this bot. " +
					"Stop the tether gateway, then run tether setup again")
			}
			return nil, fmt.Errorf("setup: getUpdates failed: %s", w.redactor(token).Redact(err.Error()))
		}
		for _, u := range ups {
			offset = max(offset, u.UpdateID+1)
			m := u.Message
			if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type != "private" {
				continue
			}
			if cmd, _, _ := strings.Cut(strings.TrimSpace(m.Text), " "); cmd == "/start" || strings.HasPrefix(cmd, "/start@") {
				// Confirm the updates, so the gateway does not get this /start again.
				cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
				_, _ = c.GetUpdates(cctx, offset, 0)
				ccancel()
				return m.From, nil
			}
		}
		if len(ups) == 0 {
			// A fake or a proxy can answer at once. Do not call the API in a tight loop.
			select {
			case <-ctx.Done():
				return nil, nil
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}

// deadline returns the deadline of ctx. The wizard always sets one.
func deadline(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

// approveOwner shows the pairing code of the user, asks for a confirmation, approves the
// user, and adds the user to telegram.allowed_users.
func (w *Wizard) approveOwner(ctx context.Context, from *telegram.User) error {
	uid := strconv.FormatInt(from.ID, 10)
	name := strings.TrimSpace(from.FirstName + " " + from.LastName)
	if name == "" {
		name = from.Username
	}
	sdb, err := db.Open(filepath.Join(w.DataDir, "state.db"))
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	defer sdb.Close()
	now := w.Now
	if now == nil {
		now = time.Now
	}
	pair := &telegram.Pairing{DB: sdb, Now: now}
	code, _, err := pair.Request(ctx, "telegram", uid, name)
	switch {
	case err != nil:
		w.say("No pairing code is available now (%v). The approval adds the user to telegram.allowed_users only.", err)
	case code != "":
		w.say("Pairing code: %s", code)
	default:
		w.say("%s (%s) is approved already.", name, uid)
	}
	yes, err := w.confirm(fmt.Sprintf("Approve %s (%s) as the owner?", name, uid))
	if err != nil {
		return err
	}
	w.cfg.setBool(true, "telegram", "enabled")
	if !yes {
		w.say("The owner is not set.")
		if code != "" {
			w.say("To approve the user later, run: tether pairing approve telegram %s", code)
		}
		return w.save()
	}
	if code != "" {
		if _, err := pair.Approve(ctx, "telegram", code); err != nil {
			return fmt.Errorf("setup: cannot approve the pairing code: %w", err)
		}
	}
	users := w.cfg.getList("telegram", "allowed_users")
	if !slices.Contains(users, uid) {
		users = append(users, uid)
	}
	w.cfg.setList(users, "telegram", "allowed_users")
	if err := w.save(); err != nil {
		return err
	}
	w.say("%s (%s) is the owner. The gateway accepts the messages of this user.", name, uid)
	return nil
}

// extrasStep shows which host programs are present and the install hint of each missing
// program.
func (w *Wizard) extrasStep() {
	cfg, err := config.Load(w.configPath(), security.EnvLookup(w.dotenv))
	if err != nil {
		def := config.Default()
		cfg = &def
	}
	d := &Doctor{DataDir: w.DataDir, LookPath: w.LookPath}
	fmt.Fprint(w.Out, Format(d.HostChecks(cfg)))
}
