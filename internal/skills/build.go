package skills

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/dnwandana/tether-agent/internal/config"
)

// NewInstaller returns an installer for the data folder dataDir with the config cfg. The
// CLI and the dashboard use it, so both get the same skill folders and git time limit.
func NewInstaller(dataDir string, cfg *config.Config, p Prompter, out io.Writer, cronUsers func(ctx context.Context) (map[string][]string, error)) (*Installer, error) {
	if dataDir == "" {
		return nil, errors.New("skills: the data folder is not set")
	}
	var external []string
	timeoutS := 0
	if cfg != nil {
		external = cfg.Skills.ExternalDirs
		timeoutS = cfg.Skills.GitTimeoutS
	}
	return &Installer{
		SkillsDir: filepath.Join(dataDir, "skills"),
		External:  external,
		FindGit:   findGitWithin(timeoutS),
		Prompt:    p,
		Out:       out,
		CronUsers: cronUsers,
	}, nil
}

// findGitWithin returns a function that gives the git program with a limit of timeoutS
// seconds for each git call. A timeoutS of 0 or less keeps the default limit.
func findGitWithin(timeoutS int) func() (Git, error) {
	return func() (Git, error) {
		g, err := FindGit()
		if err != nil {
			return g, err
		}
		if timeoutS > 0 {
			g.Timeout = time.Duration(timeoutS) * time.Second
		}
		return g, nil
	}
}
