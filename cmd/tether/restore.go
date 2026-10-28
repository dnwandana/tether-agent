package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dnwandana/tether-agent/internal/backup"
	"github.com/dnwandana/tether-agent/internal/security"
)

// restoreStdin gives the archive for FILE "-", and else the answer to the confirmation.
// Tests replace it.
var restoreStdin io.Reader = os.Stdin

// stdinIsTerminal reports whether stdin is a terminal, so the owner can type the answer.
// Tests replace it.
var stdinIsTerminal = func() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// runRestore puts an archive from tether backup back into the data dir and returns the
// exit code. It never calls app.New. backup.Restore holds the locks that keep the other
// tether processes away from the data dir.
func runRestore(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("restore", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	yesFlag := fl.Bool("yes", false, "do not ask for the confirmation")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "restore", `an archive is needed: FILE, or "-" for stdin`)
	}
	if len(rest) > 1 {
		return usageError(stderr, "restore", "unexpected argument: "+rest[1])
	}
	file := rest[0]
	if file == "-" && !*yesFlag {
		return usageError(stderr, "restore", `"-" needs --yes, because stdin holds the archive`)
	}
	if !*yesFlag && !stdinIsTerminal() {
		return usageError(stderr, "restore", "stdin is not a terminal; use --yes")
	}
	dir := dataDir(*dataFlag)

	// .env is not in the archive and the restore does not change it. Load it first, so a bad
	// .env stops the command before a change.
	envPath := filepath.Join(dir, ".env")
	dotenv, err := security.LoadDotEnv(envPath)
	if err != nil {
		fmt.Fprintf(stderr, "restore: %v\n", err)
		return 1
	}

	archive, source := restoreStdin, "stdin"
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			fmt.Fprintf(stderr, "restore: %v\n", err)
			return 1
		}
		defer f.Close()
		archive, source = f, filepath.Base(file)
	}

	// A signal cancels the context, so the stage step stops and deletes its folder.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var confirm func(backup.Summary) bool
	if !*yesFlag {
		answers := bufio.NewReader(restoreStdin)
		confirm = func(s backup.Summary) bool {
			printRestoreSummary(stdout, s)
			fmt.Fprint(stdout, `Type "restore" to continue: `)
			// Read in a goroutine, so a signal stops the wait for the answer.
			lines := make(chan string, 1)
			go func() {
				line, err := answers.ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					line = ""
				}
				lines <- line
			}()
			select {
			case line := <-lines:
				return strings.TrimRight(line, "\r\n") == "restore"
			case <-ctx.Done():
				fmt.Fprintln(stdout)
				return false
			}
		}
	}

	res, err := backup.Restore(ctx, backup.RestoreOptions{
		DataDir: dir,
		Archive: archive,
		Confirm: confirm,
		Now:     time.Now,
	})
	if err != nil {
		fmt.Fprintf(stderr, "restore: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Restored from %s.\n", source)
	if res.PreRestoreDir != "" {
		fmt.Fprintf(stdout, "The old data is in %s/.\n", res.PreRestoreDir)
		fmt.Fprintln(stdout, "Delete that folder when you no longer need it.")
	}
	lookup := func(name string) (string, bool) {
		if v, ok := dotenv[name]; ok {
			return v, true
		}
		return os.LookupEnv(name)
	}
	vars, err := backup.RequiredVars(filepath.Join(dir, "config.yaml"), lookup)
	if err != nil {
		// The restore is complete. Only the variable list is not available.
		fmt.Fprintf(stderr, "restore: cannot read config.yaml for the variable list: %v\n", err)
	} else {
		printRestoreVars(stdout, vars)
	}
	if _, err := os.Stat(envPath); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintln(stdout, "Create .env, or run tether setup.")
	}
	fmt.Fprintln(stdout, "Start the gateway: systemctl --user start tether")
	return 0
}

// printRestoreVars writes the status of each ${NAME} in the restored config.yaml. When no
// name is missing, it writes one line that says so. With no names, it writes nothing.
func printRestoreVars(w io.Writer, vars []backup.VarStatus) {
	if len(vars) == 0 {
		return
	}
	missing := false
	for _, v := range vars {
		if !v.Set {
			missing = true
		}
	}
	if !missing {
		fmt.Fprintln(w, "All variables in config.yaml are set.")
		return
	}
	fmt.Fprintln(w, "config.yaml needs these variables in .env:")
	for _, v := range vars {
		status := "not set"
		if v.Set {
			status = "set"
		}
		fmt.Fprintf(w, "  %-21s %s\n", v.Name, status)
	}
}

// printRestoreSummary writes what the archive holds and what the restore moves, so the
// owner can stop before a change.
func printRestoreSummary(w io.Writer, s backup.Summary) {
	m := s.Manifest
	fmt.Fprintf(w, "Backup from %s, tether %s, schema %d.\n",
		m.CreatedAt.Local().Format("2006-01-02 15:04:05"), m.TetherVersion, m.SchemaVersion)
	fmt.Fprintf(w, "%-14s%s, %s, %s\n", "state.db",
		countNoun(s.Stats.Messages, "message", "messages"),
		countNoun(s.Stats.Sessions, "session", "sessions"),
		countNoun(s.Stats.CronJobs, "cron job", "cron jobs"))

	c := countItems(m.Files)
	if c.hasConfig {
		fmt.Fprintf(w, "%-14s%s\n", "config.yaml", countNoun(1, "file", "files"))
	}
	c.printFolders(w)
	fmt.Fprintf(w, "The restore replaces the data in %s.\n", s.DataDir)
	fmt.Fprintf(w, "If the data dir has old data, the old data moves to %s/.\n", s.PreRestoreDir)
}
