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

// Run runs the setup steps: the provider, the fallback provider, Telegram, the host
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
	if err := w.providerStep(ctx, "primary", "PRIMARY_API_KEY"); err != nil {
		return err
	}

	w.say("\nStep 2 of 5: the fallback provider (optional)")
	q := "Set up a fallback provider?"
	if w.cfg.has("providers", "fallback") {
		q = "Change the fallback provider?"
	}
	if yes, err := w.confirm(q); err != nil {
		return err
	} else if yes {
		if err := w.providerStep(ctx, "fallback", "FALLBACK_API_KEY"); err != nil {
			return err
		}
	}

	w.say("\nStep 3 of 5: Telegram (optional)")
	q = "Set up Telegram?"
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
	format, baseURL, key, model string
	// keyRef is the variable of the key in the config.
	keyRef string
	// keyFromUser is true when key must go to .env under keyRef.
	keyFromUser bool
}

// providerStep asks for one provider, tests it, and saves it. A failed test call asks the
// questions one more time. A second failure stops the wizard.
func (w *Wizard) providerStep(ctx context.Context, name, keyVar string) error {
	a := w.currentProvider(name, keyVar)
	for try := 1; ; try++ {
		var err error
		if a, err = w.askProvider(ctx, a); err != nil {
			return err
		}
		model, ms, err := TestProvider(ctx, name, config.Provider{Format: a.format, BaseURL: a.baseURL, APIKey: a.key, Model: a.model}, w.hc())
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
	w.cfg.setString(a.format, "providers", name, "format")
	w.cfg.setString(a.baseURL, "providers", name, "base_url")
	w.cfg.setString("${"+a.keyRef+"}", "providers", name, "api_key")
	w.cfg.setString(a.model, "providers", name, "model")
	return w.save()
}

// currentProvider returns the current values of a provider entry as the defaults.
func (w *Wizard) currentProvider(name, keyVar string) providerAnswers {
	a := providerAnswers{
		format:  w.cfg.get("providers", name, "format"),
		baseURL: w.cfg.get("providers", name, "base_url"),
		model:   w.cfg.get("providers", name, "model"),
		keyRef:  keyVar,
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

// askProvider asks the four provider questions with a as the defaults.
func (w *Wizard) askProvider(ctx context.Context, a providerAnswers) (providerAnswers, error) {
	def := a.format
	if def == "" {
		def = "openai"
	}
	for {
		a.format = strings.ToLower(w.ask("Format (openai or anthropic)", def))
		if a.format == "openai" || a.format == "anthropic" {
			break
		}
		if w.eof {
			return a, errInputEnded
		}
		w.say("The format must be openai or anthropic.")
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
	keyQ := "API key (or ${NAME} for a variable in .env)"
	if a.key != "" {
		keyQ += " [Enter keeps the current key]"
	}
	v, err := w.askSecret(ctx, keyQ)
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
	if a.model, err = w.askRequired("Model", a.model); err != nil {
		return a, err
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
