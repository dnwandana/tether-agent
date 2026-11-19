package app

import (
	"os"
	"slices"
	"strings"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// skillPassthrough returns the secret names that a child process keeps:
// terminal.env_passthrough only. The owner writes that list. A skill cannot
// add a name, because the model can declare names in its own skill.
func skillPassthrough(cfg *config.Config) []string {
	return slices.Clone(cfg.Terminal.EnvPassthrough)
}

// boundCommands returns the command prefixes of each name of exec_secrets, or nil for none.
func boundCommands(cfg *config.Config) map[string][]string {
	if len(cfg.ExecSecrets) == 0 {
		return nil
	}
	out := make(map[string][]string, len(cfg.ExecSecrets))
	for name, s := range cfg.ExecSecrets {
		out[name] = s.Commands
	}
	return out
}

// boundNames returns the names of exec_secrets, sorted.
func boundNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.ExecSecrets))
	for name := range cfg.ExecSecrets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// envStatus returns one status per name: "bound: <commands>" for a bound name with a .env
// value, "not set" when no value exists, "passed" when a child process keeps the name, and a
// "not passed" hint when the name is a secret that the owner did not list. A bound name reads
// .env only.
func envStatus(cfg *config.Config, store *security.DotEnvStore, names []string) []string {
	dotenv := store.Snapshot()
	out := make([]string, 0, len(names))
	for _, name := range names {
		if es, ok := cfg.ExecSecrets[name]; ok {
			if dotenv[name] == "" {
				out = append(out, "not set")
			} else {
				out = append(out, "bound: "+strings.Join(es.Commands, ", "))
			}
			continue
		}
		v, _ := os.LookupEnv(name)
		switch {
		case v == "" && dotenv[name] == "":
			out = append(out, "not set")
		case slices.Contains(cfg.Terminal.EnvPassthrough, name) || !security.IsSecretName(name, dotenv):
			out = append(out, "passed")
		default:
			out = append(out, "not passed: add it to terminal.env_passthrough in config.yaml")
		}
	}
	return out
}
