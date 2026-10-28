package backup

import (
	"errors"
	"io/fs"
	"os"
	"slices"
)

// VarStatus tells if one variable that config.yaml refers to has a value.
type VarStatus struct {
	Name string
	Set  bool
}

// RequiredVars returns each ${NAME} in the config file at configPath, with
// its status from lookup. The names are unique and in sorted order.
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
