package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/backup"
	"github.com/dnwandana/tether-agent/internal/security"
)

// runBackup writes the state of the data dir to one archive and returns the exit code. It
// opens only state.db and never calls app.New, so it runs next to a gateway.
func runBackup(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	outFlag := fl.String("out", "", `archive path, or "-" for stdout (default: <data dir>/backups/)`)
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) > 0 {
		return usageError(stderr, "backup", "unexpected argument: "+rest[0])
	}
	dir := dataDir(*dataFlag)
	// With --out -, stdout holds only the archive bytes, so the report goes to stderr.
	report := stdout
	if *outFlag == "-" {
		report = stderr
	}
	dotenv, err := security.LoadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		fmt.Fprintf(stderr, "backup: %v\n", err)
		return 1
	}
	// A signal cancels the context, so the deferred cleanup of the temp files runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	res, err := backup.Create(ctx, backup.Options{
		DataDir: dir,
		OutPath: *outFlag,
		Stdout:  stdout,
		DotEnv:  dotenv,
		Environ: os.Environ(),
		Version: version,
		Now:     time.Now,
	})
	if err != nil {
		fmt.Fprintf(stderr, "backup: %v\n", err)
		return 1
	}
	printBackupReport(report, res)
	return 0
}

// printBackupReport writes the report of a backup that is complete. It names key paths and
// counts, never a secret value. A line with a zero count does not show.
func printBackupReport(w io.Writer, r *backup.Result) {
	c := countItems(r.Manifest.Files)
	fmt.Fprintf(w, "%-14s%s  (%s, %s)\n", "state.db", formatSize(c.dbSize),
		countNoun(r.Stats.Messages, "message", "messages"), countNoun(r.Stats.Sessions, "session", "sessions"))
	c.printFolders(w)
	if n := len(r.Replaced); n > 0 {
		fmt.Fprintf(w, "%-14s%s replaced\n", "config.yaml", countNoun(n, "literal secret", "literal secrets"))
		for _, rep := range r.Replaced {
			fmt.Fprintf(w, "  %s -> ${%s}\n", rep.KeyPath, rep.Name)
		}
		fmt.Fprintln(w, "  The restore needs these names in .env.")
	}
	if n := len(r.Warnings); n > 0 {
		verb := "values look like a secret and are"
		if n == 1 {
			verb = "value looks like a secret and is"
		}
		fmt.Fprintf(w, "%-14s%d %s in the archive\n", "config.yaml", n, verb)
		for _, key := range r.Warnings {
			fmt.Fprintf(w, "  %s\n", key)
		}
		fmt.Fprintln(w, "  Move the secret to .env, and use ${NAME} in config.yaml.")
	}
	if c.hasConfig && len(r.Replaced) == 0 && len(r.Warnings) == 0 {
		fmt.Fprintf(w, "%-14s%s\n", "config.yaml", "saved with no change")
	}
	if n := r.Manifest.Redacted.StateDB; n > 0 {
		fmt.Fprintf(w, "Masked %s in state.db.\n", countNoun(n, "known secret value", "known secret values"))
	}
	if n := r.Manifest.Redacted.Files; n > 0 {
		fmt.Fprintf(w, "Masked the known secret values in %s.\n", countNoun(n, "text file", "text files"))
	}
	for _, rel := range r.Skipped {
		fmt.Fprintf(w, "Skipped the link %s.\n", rel)
	}
	if r.Path == "-" {
		fmt.Fprintf(w, "Backup written to stdout (%s)\n", formatSize(r.Size))
	} else {
		fmt.Fprintf(w, "Backup saved: %s (%s)\n", r.Path, formatSize(r.Size))
	}
	fmt.Fprintln(w, "The archive holds your messages. Keep it private.")
	fmt.Fprintln(w, "The archive does not hold .env. Copy .env on its own when you move to a new host.")
}

// itemCounts holds the counts of the archive entries that the backup report and the restore
// summary show.
type itemCounts struct {
	dbSize                      int64
	hasConfig                   bool
	memory, scripts, skillFiles int
	skillDirs                   map[string]bool
}

// countItems counts the files of a manifest by the folder of the allow list.
func countItems(files []backup.FileEntry) itemCounts {
	c := itemCounts{skillDirs: map[string]bool{}}
	for _, f := range files {
		switch {
		case f.Path == "state.db":
			c.dbSize = f.Size
		case f.Path == "config.yaml":
			c.hasConfig = true
		case strings.HasPrefix(f.Path, "memory/"):
			c.memory++
		case strings.HasPrefix(f.Path, "scripts/"):
			c.scripts++
		case strings.HasPrefix(f.Path, "skills/"):
			c.skillFiles++
			// A skill is a folder directly in skills/.
			if name, _, ok := strings.Cut(strings.TrimPrefix(f.Path, "skills/"), "/"); ok {
				c.skillDirs[name] = true
			}
		}
	}
	return c
}

// printFolders writes one line for each folder of the allow list that has files.
func (c itemCounts) printFolders(w io.Writer) {
	if c.memory > 0 {
		fmt.Fprintf(w, "%-14s%s\n", "memory/", countNoun(c.memory, "file", "files"))
	}
	if c.skillFiles > 0 {
		text := countNoun(c.skillFiles, "file", "files")
		if len(c.skillDirs) > 0 {
			text = countNoun(len(c.skillDirs), "skill", "skills") + ", " + text
		}
		fmt.Fprintf(w, "%-14s%s\n", "skills/", text)
	}
	if c.scripts > 0 {
		fmt.Fprintf(w, "%-14s%s\n", "scripts/", countNoun(c.scripts, "file", "files"))
	}
}

// formatSize returns n bytes in KB below 1 MB, else in MB, with one decimal.
func formatSize(n int64) string {
	const kb, mb = 1024, 1024 * 1024
	if n < mb {
		return strconv.FormatFloat(float64(n)/kb, 'f', 1, 64) + " KB"
	}
	return strconv.FormatFloat(float64(n)/mb, 'f', 1, 64) + " MB"
}

// countNoun returns n with a thousands separator and the singular or the plural noun.
func countNoun(n int, one, many string) string {
	noun := many
	if n == 1 {
		noun = one
	}
	return groupDigits(n) + " " + noun
}

// groupDigits returns the count n with a comma between each group of three digits.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
