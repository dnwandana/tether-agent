package backup

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dnwandana/tether-agent/internal/config"
)

// VarStatus tells if one variable that config.yaml refers to has a value.
type VarStatus struct {
	Name string
	Set  bool
}

// RequiredVars returns each ${NAME} in the config file at configPath, with
// its status from lookup. The names are unique and in sorted order.
//
// A provider entry with a known preset, no api_key, and the preset URL (or no
// base_url) reads its key from the KeyEnv of the preset. RequiredVars adds
// that name too. A YAML error does not stop the report: the list then has
// only the ${NAME} references.
//
// config.Load stops at the first missing name. The restore report uses this
// function to list all the missing names at one time. A missing file returns
// nil and no error.
func RequiredVars(configPath string, lookup func(string) (string, bool)) ([]VarStatus, error) {
	b, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range envVarRef.FindAllSubmatch(b, -1) {
		names = append(names, string(m[1]))
	}
	names = append(names, presetKeyEnvs(b)...)
	if len(names) == 0 {
		return nil, nil
	}
	slices.Sort(names)
	names = slices.Compact(names)
	out := make([]VarStatus, len(names))
	for i, n := range names {
		_, set := lookup(n)
		out[i] = VarStatus{Name: n, Set: set}
	}
	return out, nil
}

// presetKeyEnvs returns the KeyEnv of each provider entry that uses the
// implicit key of its preset. It does not expand ${NAME}, because only the
// presence of api_key is important. A YAML error returns nil.
func presetKeyEnvs(b []byte) []string {
	var doc struct {
		Providers map[string]struct {
			Preset  string `yaml:"preset"`
			BaseURL string `yaml:"base_url"`
			APIKey  string `yaml:"api_key"`
		} `yaml:"providers"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil
	}
	var out []string
	for _, e := range doc.Providers {
		p, ok := config.PresetByID(e.Preset)
		if !ok || e.APIKey != "" {
			continue
		}
		if e.BaseURL != "" && strings.TrimRight(e.BaseURL, "/") != strings.TrimRight(p.BaseURL, "/") {
			continue
		}
		out = append(out, p.KeyEnv)
	}
	return out
}
