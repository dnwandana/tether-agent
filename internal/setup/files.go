package setup

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// configDoc is config.yaml as a YAML node tree. An edit through the tree keeps the
// comments and the keys that the wizard does not know.
type configDoc struct {
	root *yaml.Node
}

// readConfigDoc reads the config file at path. A missing or empty file gives an empty
// mapping.
func readConfigDoc(path string) (*configDoc, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("setup: cannot read %s: %w", path, err)
	}
	doc := &yaml.Node{Kind: yaml.DocumentNode}
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, doc); err != nil {
			return nil, fmt.Errorf("setup: %s is not valid YAML: %w. Correct the file or move it away, then run setup again", path, err)
		}
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	if doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("setup: %s is not a YAML mapping. Correct the file or move it away, then run setup again", path)
	}
	return &configDoc{root: doc}, nil
}

// get returns the scalar value at the key path, or "" when the path does not exist.
func (c *configDoc) get(keys ...string) string {
	n := c.root.Content[0]
	for _, k := range keys {
		n = mapValue(n, k)
		if n == nil {
			return ""
		}
	}
	if n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// getList returns the scalar items of the sequence at the key path.
func (c *configDoc) getList(keys ...string) []string {
	n := c.root.Content[0]
	for _, k := range keys {
		n = mapValue(n, k)
		if n == nil {
			return nil
		}
	}
	var out []string
	if n.Kind == yaml.SequenceNode {
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode {
				out = append(out, item.Value)
			}
		}
	}
	return out
}

// has reports whether the key path exists.
func (c *configDoc) has(keys ...string) bool {
	n := c.root.Content[0]
	for _, k := range keys {
		n = mapValue(n, k)
		if n == nil {
			return false
		}
	}
	return true
}

// setString sets the string at the key path. It makes the mappings that are missing.
func (c *configDoc) setString(value string, keys ...string) {
	c.set(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}, keys...)
}

// setBool sets the boolean at the key path.
func (c *configDoc) setBool(value bool, keys ...string) {
	c.set(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(value)}, keys...)
}

// setList sets a sequence of quoted strings at the key path.
func (c *configDoc) setList(values []string, keys ...string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: yaml.DoubleQuotedStyle})
	}
	c.set(seq, keys...)
}

// set puts value at the key path. An existing value keeps its comments.
func (c *configDoc) set(value *yaml.Node, keys ...string) {
	n := c.root.Content[0]
	for i, k := range keys {
		last := i == len(keys)-1
		v := mapValue(n, k)
		switch {
		case v == nil:
			next := value
			if !last {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, next)
			n = next
		case last:
			value.HeadComment, value.LineComment, value.FootComment = v.HeadComment, v.LineComment, v.FootComment
			*v = *value
		case v.Kind != yaml.MappingNode:
			// A null or a scalar where a mapping must be: replace it.
			*v = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: v.HeadComment, LineComment: v.LineComment}
			n = v
		default:
			n = v
		}
	}
}

// mapValue returns the value node of key in the mapping m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// bytes returns the YAML text of the document.
func (c *configDoc) bytes() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(c.root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// envFile is .env as lines. An edit keeps the comments and the other lines.
type envFile struct {
	lines []string
}

// readEnvFile reads the .env file at path. A missing file gives an empty file.
func readEnvFile(path string) (*envFile, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &envFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("setup: cannot read %s: %w", path, err)
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return &envFile{}, nil
	}
	return &envFile{lines: strings.Split(text, "\n")}, nil
}

// envLineName returns the variable name of a NAME=value line, or "".
func envLineName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	name, _, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

// set sets NAME=value. It replaces the line of the name, or adds a line at the end. It
// reports whether the file changed.
func (e *envFile) set(name, value string) bool {
	line := name + "=" + quoteEnvValue(value)
	for i, l := range e.lines {
		if envLineName(l) == name {
			if l == line {
				return false
			}
			e.lines[i] = line
			return true
		}
	}
	e.lines = append(e.lines, line)
	return true
}

// quoteEnvValue returns the value in the .env syntax of security.LoadDotEnv. A value with
// spaces, quotes, "#", or a backslash goes in double quotes with escapes.
func quoteEnvValue(v string) string {
	if v == "" || !strings.ContainsAny(v, " \t#\"'\\\n") {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(v) + `"`
}

func (e *envFile) bytes() []byte {
	if len(e.lines) == 0 {
		return nil
	}
	return []byte(strings.Join(e.lines, "\n") + "\n")
}

// writeSecretFile writes data to path with mode 0600 through a temporary file and a
// rename. When backup is true and path exists, it first copies path to path.bak.
func writeSecretFile(path string, data []byte, backup bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("setup: cannot make the folder of %s: %w", path, err)
	}
	if backup {
		old, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := atomicWrite(path+".bak", old, 0o600); err != nil {
				return err
			}
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("setup: cannot read %s for the backup: %w", path, err)
		}
	}
	return atomicWrite(path, data, 0o600)
}

// atomicWrite writes data to path through a temporary file in the same folder and a
// rename, so a reader never sees half a file.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("setup: cannot write %s: %w", path, err)
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("setup: cannot write %s: %w", path, err)
	}
	return nil
}
