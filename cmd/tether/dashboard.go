package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/dnwandana/tether-agent/internal/dashboard"
	"github.com/dnwandana/tether-agent/internal/db"
)

// The texts of the dashboard subcommand.
const (
	dashNoTerminal = "%s: run this command in a terminal on the host.\n"
	dashOff        = "dashboard: off. Set dashboard.enabled: true and restart the gateway.\n"
	dashCodeLine   = "The code works once and expires in 5 minutes.\n"
	dashWildcard   = "dashboard.listen is a wildcard address. Put the address that your browser uses in front of this path.\n"
	dashNoDevices  = "No device is signed in.\n"
)

// dashCodeTTL is the life of a code of "tether dashboard link".
const dashCodeTTL = 5 * time.Minute

// runDashboard runs the dashboard subcommand and returns the exit code. It opens state.db and
// does not need the gateway.
func runDashboard(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	dataFlag := fl.String("data-dir", "", "data folder (default: TETHER_HOME, then ~/.tether)")
	rest, code, ok := parseCommand(fl, args, stdout, stderr)
	if !ok {
		return code
	}
	if len(rest) == 0 {
		return usageError(stderr, "dashboard", "a command is needed: link, devices, or signout-all")
	}
	cmd := rest[0]
	switch cmd {
	case "link", "devices", "signout-all":
	default:
		return usageError(stderr, "dashboard", "unknown command: "+cmd)
	}
	if len(rest) > 1 {
		return usageError(stderr, "dashboard", "unexpected argument: "+rest[1])
	}
	dir := dataDir(*dataFlag)
	ctx := context.Background()
	switch cmd {
	case "link":
		return dashboardLink(ctx, dir, stdout, stderr)
	case "devices":
		return dashboardDevices(ctx, dir, stdout, stderr)
	default:
		return dashboardSignoutAll(ctx, dir, stdout, stderr)
	}
}

// openDashStore opens the existing state.db of dir and returns the device store. The caller
// calls closeFn when it is done.
func openDashStore(dir, name string, stderr io.Writer) (st *dashboard.Store, closeFn func(), ok bool) {
	dbPath := filepath.Join(dir, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(stderr, "%s: cannot open %s: %v\n", name, dbPath, err)
		return nil, nil, false
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return nil, nil, false
	}
	return dashboard.NewStore(d), func() { d.Close() }, true
}

// dashboardLink prints a sign-in link with a new one-time code.
func dashboardLink(ctx context.Context, dir string, stdout, stderr io.Writer) int {
	if !stdioIsTerminal() {
		fmt.Fprintf(stderr, dashNoTerminal, "dashboard link")
		return 1
	}
	cfg, err := skillsConfig(dir)
	if err != nil {
		fmt.Fprintf(stderr, "dashboard link: %v\n", err)
		return 1
	}
	dc := cfg.Dashboard
	if !dc.Enabled {
		fmt.Fprint(stderr, dashOff)
		return 1
	}
	if err := dc.Check(); err != nil {
		fmt.Fprintf(stderr, "dashboard link: %v\n", err)
		return 1
	}
	d, err := db.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		fmt.Fprintf(stderr, "dashboard link: %v\n", err)
		return 1
	}
	defer d.Close()
	code, err := dashboard.NewStore(d).AddCode(ctx, dashCodeTTL, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "dashboard link: %v\n", err)
		return 1
	}
	if dc.PublicURL == "" && wildcardListen(dc.Listen) {
		fmt.Fprintf(stdout, "/#code=%s\n%s", code, dashWildcard)
	} else {
		fmt.Fprintf(stdout, "%s/#code=%s\n", dc.Origin(), code)
	}
	fmt.Fprint(stdout, dashCodeLine)
	return 0
}

// dashboardDevices prints the signed-in devices as a table.
func dashboardDevices(ctx context.Context, dir string, stdout, stderr io.Writer) int {
	st, closeFn, ok := openDashStore(dir, "dashboard devices", stderr)
	if !ok {
		return 1
	}
	defer closeFn()
	devs, err := st.List(ctx, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "dashboard devices: %v\n", err)
		return 1
	}
	if len(devs) == 0 {
		fmt.Fprint(stdout, dashNoDevices)
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDEVICE\tIP\tSIGNED IN\tLAST SEEN\tEXPIRES")
	for _, d := range devs {
		id := d.ID
		if len(id) > 8 {
			id = id[:8]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", id, d.Label, d.IP,
			d.Created.Local().Format(time.DateTime), d.LastSeen.Local().Format(time.DateTime),
			d.Expires.Local().Format(time.DateTime))
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintf(stderr, "dashboard devices: %v\n", err)
		return 1
	}
	return 0
}

// dashboardSignoutAll ends every device session.
func dashboardSignoutAll(ctx context.Context, dir string, stdout, stderr io.Writer) int {
	if !stdioIsTerminal() {
		fmt.Fprintf(stderr, dashNoTerminal, "dashboard signout-all")
		return 1
	}
	st, closeFn, ok := openDashStore(dir, "dashboard signout-all", stderr)
	if !ok {
		return 1
	}
	defer closeFn()
	n, err := st.RevokeAll(ctx, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "dashboard signout-all: %v\n", err)
		return 1
	}
	if n == 1 {
		fmt.Fprintln(stdout, "Ended 1 device session. Each device must sign in again.")
	} else {
		fmt.Fprintf(stdout, "Ended %d device sessions. Each device must sign in again.\n", n)
	}
	return 0
}

// wildcardListen reports whether the host of the listen address is empty, 0.0.0.0, or ::.
func wildcardListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsUnspecified()
	}
	return host == ""
}
