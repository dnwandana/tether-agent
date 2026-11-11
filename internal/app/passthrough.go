package app

import (
	"os"
	"slices"

	"github.com/dnwandana/tether-agent/internal/config"
	"github.com/dnwandana/tether-agent/internal/security"
)

// skillPassthrough returns the secret names that a child process keeps:
// terminal.env_passthrough only. The owner writes that list. A skill cannot
// add a name, because the model can declare names in its own skill.
func skillPassthrough(cfg *config.Config) []string {
	return slices.Clone(cfg.Terminal.EnvPassthrough)
}

// envStatus returns one status per name: "not set" when no value exists in the
// process or in dotenv, "passed" when a child process keeps the name, and a
// "not passed" hint when the name is a secret that the owner did not list.
func envStatus(cfg *config.Config, dotenv map[string]string, names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
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
