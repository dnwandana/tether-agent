package backup

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/dnwandana/tether-agent/internal/security"
)

// envVarRef matches one ${NAME} reference. It is the same regex as the one
// in config.Load, so the backup, the restore report, and the load agree on
// what a reference is.
var envVarRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Replacement is one literal secret of config.yaml that the archive copy
// holds as a ${Name} reference.
type Replacement struct {
	KeyPath, Name string
}

// ConfigResult is the archive copy of config.yaml.
type ConfigResult struct {
	// Data is the redacted file. It is the input itself when nothing changed.
	Data []byte
	// Replaced lists the literal secrets in document order.
	Replaced []Replacement
	// Warnings holds the key paths of the values that look like a secret
	// and stay in the archive. It never holds a value.
	Warnings []string
}

// redactConfig returns the archive copy of config.yaml. A literal value in a
// secret key path becomes a ${NAME} reference. A known secret value in any
// other string also becomes a ${NAME} reference, with the name of its
// variable from known (value to name). config.Load expands ${NAME} in each
// string, so the value works again after a restore. A value that still looks
// like a secret gives a warning and does not change.
func redactConfig(data []byte, dotenv map[string]string, lookupEnv func(string) (string, bool), known map[string]string) (ConfigResult, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ConfigResult{}, configError(err)
	}
	c := &configRedactor{
		dotenv:    dotenv,
		lookupEnv: lookupEnv,
		refs:      refReplacer(known),
		byValue:   map[string]string{},
		byName:    map[string]string{},
	}
	c.walk(&doc, nil)
	res := ConfigResult{Data: data, Replaced: c.replaced, Warnings: c.warnings}
	if !c.changed {
		return res, nil
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return ConfigResult{}, fmt.Errorf("cannot encode the redacted config.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return ConfigResult{}, fmt.Errorf("cannot encode the redacted config.yaml: %w", err)
	}
	res.Data = b.Bytes()
	return res, nil
}

// configError keeps the yaml error text and drops a typed error value, which
// can quote the content of the file.
func configError(err error) error {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		return fmt.Errorf("config.yaml is not valid YAML: %d errors", len(te.Errors))
	}
	return fmt.Errorf("config.yaml is not valid YAML: %v", err)
}

// refReplacer returns a replacer that changes each known value of 8 or more characters to its
// ${NAME} reference, or nil when there is no such value. The 8-character rule is the rule of
// security.NewRedactor. The longest value goes first, so it wins where two values overlap.
func refReplacer(known map[string]string) *strings.Replacer {
	vals := make([]string, 0, len(known))
	for v := range known {
		if utf8.RuneCountInString(v) >= 8 {
			vals = append(vals, v)
		}
	}
	if len(vals) == 0 {
		return nil
	}
	slices.SortFunc(vals, func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b))
	})
	pairs := make([]string, 0, 2*len(vals))
	for _, v := range vals {
		pairs = append(pairs, v, "${"+known[v]+"}")
	}
	return strings.NewReplacer(pairs...)
}

type configRedactor struct {
	dotenv    map[string]string
	lookupEnv func(string) (string, bool)
	// refs changes each known secret value to its ${NAME} reference. It is nil when there is
	// no known value.
	refs *strings.Replacer
	// byValue maps a literal to the name that this run gave it.
	byValue map[string]string
	// byName maps a name that this run gave to its literal.
	byName   map[string]string
	replaced []Replacement
	warnings []string
	changed  bool
}

// walk visits the nodes in document order. path holds the mapping keys and
// the sequence indexes from the root to n.
func (c *configRedactor) walk(n *yaml.Node, path []string) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, ch := range n.Content {
			c.walk(ch, path)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			c.walk(n.Content[i+1], append(slices.Clip(path), n.Content[i].Value))
		}
	case yaml.SequenceNode:
		for i, ch := range n.Content {
			c.walk(ch, append(slices.Clip(path), "["+strconv.Itoa(i)+"]"))
		}
	case yaml.ScalarNode:
		c.scalar(n, path)
	}
}

func (c *configRedactor) scalar(n *yaml.Node, path []string) {
	if n.ShortTag() == "!!null" {
		return
	}
	key := joinKeyPath(path)
	if derived, ok := secretName(path); ok && n.Value != "" && !strings.Contains(n.Value, "${") {
		name := c.nameFor(n.Value, derived)
		n.Value = "${" + name + "}"
		n.Tag = "!!str"
		c.changed = true
		c.replaced = append(c.replaced, Replacement{KeyPath: key, Name: name})
		return
	}
	if n.ShortTag() != "!!str" {
		return
	}
	if c.refs != nil {
		if v := c.refs.Replace(n.Value); v != n.Value {
			for _, name := range newRefs(n.Value, v) {
				c.replaced = append(c.replaced, Replacement{KeyPath: key, Name: name})
			}
			n.Value = v
			c.changed = true
		}
	}
	if security.LooksLikeSecret(n.Value) {
		c.warnings = append(c.warnings, key)
	}
}

// newRefs returns the ${NAME} names that are in after and not in before, in order and with no
// duplicate. These are the names that refReplacer added.
func newRefs(before, after string) []string {
	old := map[string]bool{}
	for _, m := range envVarRef.FindAllStringSubmatch(before, -1) {
		old[m[1]] = true
	}
	var names []string
	for _, m := range envVarRef.FindAllStringSubmatch(after, -1) {
		if !old[m[1]] {
			old[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// nameFor returns the reference name for literal. The choice is
// deterministic: the name of an earlier key path, then the first .env name
// in sorted order, then the derived name with the first free suffix.
func (c *configRedactor) nameFor(literal, derived string) string {
	if name, ok := c.byValue[literal]; ok {
		return name
	}
	var name string
	for _, k := range sortedKeys(c.dotenv) {
		if c.dotenv[k] == literal {
			name = k
			break
		}
	}
	if name == "" {
		name = derived
		for i := 2; c.taken(name, literal); i++ {
			name = derived + "_" + strconv.Itoa(i)
		}
	}
	c.byValue[literal] = name
	c.byName[name] = literal
	return name
}

// taken reports whether .env, the environment, or this run uses name for a
// value other than literal.
func (c *configRedactor) taken(name, literal string) bool {
	if v, ok := c.dotenv[name]; ok && v != literal {
		return true
	}
	if c.lookupEnv != nil {
		if v, ok := c.lookupEnv(name); ok && v != literal {
			return true
		}
	}
	if v, ok := c.byName[name]; ok && v != literal {
		return true
	}
	return false
}

// secretName returns the derived reference name when path is one of the
// secret key paths of config.yaml.
func secretName(path []string) (string, bool) {
	switch {
	case len(path) == 3 && path[0] == "providers" && path[2] == "api_key":
		return envName(path[1]) + "_API_KEY", true
	case len(path) == 2 && path[0] == "telegram" && path[1] == "bot_token":
		return "TELEGRAM_BOT_TOKEN", true
	case len(path) == 5 && path[0] == "mcp" && path[1] == "servers" && path[3] == "env":
		return path[4], true
	case len(path) == 5 && path[0] == "mcp" && path[1] == "servers" && path[3] == "headers":
		return "MCP_" + envName(path[2]) + "_" + envName(path[4]), true
	}
	return "", false
}

// envName returns s in upper case, with each character that is not an ASCII
// letter or digit changed to "_", so that the name is a valid variable name.
func envName(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			out = append(out, byte(r))
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

// joinKeyPath returns the dotted key path. A sequence index follows its key
// with no dot, as in "args[1]".
func joinKeyPath(path []string) string {
	var b strings.Builder
	for i, p := range path {
		if i > 0 && !strings.HasPrefix(p, "[") {
			b.WriteByte('.')
		}
		b.WriteString(p)
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
