package cron

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// SkillUsers returns, for each skill name, one line for each cron job that uses the skill. A
// job uses a skill when its skills column names it or when its prompt holds the name as a
// word. Paused jobs count. A nil names makes each prompt word a key. Each line is
// `"<job name>" (<job id>)`.
func SkillUsers(ctx context.Context, s *Store, names []string) (map[string][]string, error) {
	// The query names only the columns of the first schema, so a database from before a
	// later migration still works.
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name, skills, prompt FROM cron_jobs ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	type jobRow struct {
		id, name, prompt string
		skills           []string
	}
	var jobs []jobRow
	for rows.Next() {
		var j jobRow
		var raw, prompt sql.NullString
		if err := rows.Scan(&j.id, &j.name, &raw, &prompt); err != nil {
			rows.Close()
			return nil, err
		}
		j.prompt = prompt.String
		if raw.Valid && raw.String != "" {
			if err := json.Unmarshal([]byte(raw.String), &j.skills); err != nil {
				rows.Close()
				return nil, fmt.Errorf("cron job %s: bad skills column: %w", j.id, err)
			}
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	users := map[string][]string{}
	for _, j := range jobs {
		line := fmt.Sprintf("%q (%s)", j.name, j.id)
		seen := map[string]bool{}
		add := func(key string) {
			if !seen[key] {
				seen[key] = true
				users[key] = append(users[key], line)
			}
		}
		for _, n := range j.skills {
			add(n)
		}
		words := promptWords(j.prompt)
		if names == nil {
			keys := make([]string, 0, len(words))
			for w := range words {
				keys = append(keys, w)
			}
			slices.Sort(keys)
			for _, w := range keys {
				add(w)
			}
			continue
		}
		for _, n := range names {
			if words[strings.ToLower(n)] {
				add(n)
			}
		}
	}
	return users, nil
}

// promptWords returns the lower-case words of a prompt. A word is a maximal run of letters,
// digits, "-", and "_".
func promptWords(prompt string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(prompt), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_'
	}) {
		out[w] = true
	}
	return out
}
