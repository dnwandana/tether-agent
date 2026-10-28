package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// State tells what Updater.Run found or did.
type State int

const (
	StateUpdated State = iota + 1 // the binary was replaced
	StateNewer                    // a newer release exists; nothing was installed (--check)
	StateSame                     // the versions are equal
	StateAhead                    // this binary is newer than the release
	StateDev                      // a dev build with --check
)

// versionTimeout is the longest time that "<candidate> version" can run.
const versionTimeout = 10 * time.Second

// downloadTimeout is the longest time that one download can take.
const downloadTimeout = 15 * time.Minute

// maxChecksumsBytes is the largest checksums.txt that the update downloads.
const maxChecksumsBytes = 1 << 20

// Updater replaces the running binary with a release from GitHub.
type Updater struct {
	Current string    // main.version
	GOOS    string    // "" uses runtime.GOOS
	GOARCH  string    // "" uses runtime.GOARCH
	BinPath string    // "" uses ExecutablePath()
	Client  *Client   // nil uses &Client{}
	Out     io.Writer // progress lines; nil discards
	// runVersion runs "<candidate> version" and returns its stdout. Tests replace it.
	runVersion func(ctx context.Context, candidate string) (string, error)
}

// Options selects the release and the action of Updater.Run.
type Options struct {
	Tag   string // "" means the newest release
	Check bool   // report only; never download
	Force bool   // install also when the versions are equal or Current is dev
}

// Result is the outcome of Updater.Run.
type Result struct {
	Current   string // the version of this binary
	Available string // the version of the release, without the v
	BinPath   string // the real file of the binary; empty when Run did not need it
	State     State
}

// Run fetches the release and compares it with Current. Unless Options.Check is set, it
// then downloads the archive, verifies its SHA-256, and replaces the binary. On each
// error, the old binary stays.
func (u *Updater) Run(ctx context.Context, o Options) (Result, error) {
	res := Result{Current: u.Current}
	goos, goarch := u.GOOS, u.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	// An unsupported platform fails here, before any request.
	if _, err := AssetName("0.0.0", goos, goarch); err != nil {
		return res, err
	}

	rel, err := u.fetch(ctx, o.Tag)
	if err != nil {
		return res, err
	}
	res.Available = rel.Version

	state, err := compareRelease(u.Current, rel.Version)
	if err != nil {
		return res, err
	}
	if o.Check {
		res.State = state
		return res, nil
	}
	if !o.Force {
		switch state {
		case StateDev:
			return res, errors.New("this is a dev build; use --force to replace it")
		case StateSame, StateAhead:
			res.State = state
			return res, nil
		}
	}

	asset, err := AssetName(rel.Version, goos, goarch)
	if err != nil {
		return res, err
	}
	assetURL, sumsURL := rel.Assets[asset], rel.Assets["checksums.txt"]
	if assetURL == "" {
		return res, fmt.Errorf("release %s has no asset %s", rel.Tag, asset)
	}
	if sumsURL == "" {
		return res, fmt.Errorf("release %s has no asset checksums.txt", rel.Tag)
	}

	bin := u.BinPath
	if bin == "" {
		if bin, err = ExecutablePath(); err != nil {
			return res, err
		}
	}
	res.BinPath = bin
	// This check comes before the download, so a user with no write permission does not wait.
	if err := CheckWritable(bin); err != nil {
		return res, err
	}

	out := u.Out
	if out == nil {
		out = io.Discard
	}
	sumsText, err := u.download(ctx, "checksums.txt", sumsURL, maxChecksumsBytes, nil)
	if err != nil {
		return res, err
	}
	archive, err := u.download(ctx, asset, assetURL, MaxArchiveBytes, func(size int64) {
		fmt.Fprint(out, downloadLine(asset, size))
	})
	if err != nil {
		return res, err
	}

	sums, err := ParseChecksums(sumsText)
	if err != nil {
		return res, err
	}
	want, ok := sums[asset]
	if !ok {
		return res, fmt.Errorf("checksums.txt has no entry for %s", asset)
	}
	if err := VerifySHA256(archive, want); err != nil {
		return res, err
	}
	fmt.Fprintln(out, "checksum OK")

	data, err := ExtractBinary(archive, MaxBinaryBytes)
	if err != nil {
		return res, err
	}
	wantOut := "tether " + rel.Version + "\n"
	check := func(candidate string) error {
		got, err := u.versionRunner()(ctx, candidate)
		if err != nil {
			return err
		}
		if got != wantOut {
			first, _, _ := strings.Cut(got, "\n")
			return fmt.Errorf("prints %q, want %q", first, strings.TrimSuffix(wantOut, "\n"))
		}
		return nil
	}
	if err := Install(bin, data, check); err != nil {
		return res, err
	}
	res.State = StateUpdated
	return res, nil
}

// fetch returns the newest release, or the release of tag when tag is set.
// A tag without the v prefix gets one, so "0.3.0" reads the release "v0.3.0".
func (u *Updater) fetch(ctx context.Context, tag string) (Release, error) {
	c := u.client()
	if tag == "" {
		return c.Latest(ctx)
	}
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	return c.Tag(ctx, tag)
}

// compareRelease gives the state of the current version against the release version.
// The result is never StateUpdated.
func compareRelease(cur, avail string) (State, error) {
	if IsDev(cur) {
		return StateDev, nil
	}
	n, err := Compare(cur, avail)
	if err != nil {
		return 0, fmt.Errorf("cannot compare %s with %s: %w", cur, avail, err)
	}
	switch {
	case n < 0:
		return StateNewer, nil
	case n > 0:
		return StateAhead, nil
	}
	return StateSame, nil
}

func (u *Updater) client() *Client {
	if u.Client != nil {
		return u.Client
	}
	return &Client{}
}

func (u *Updater) versionRunner() func(ctx context.Context, candidate string) (string, error) {
	if u.runVersion != nil {
		return u.runVersion
	}
	return runVersion
}

// download returns the body of url and fails when the body is larger than limit bytes.
// It calls start, when start is not nil, with resp.ContentLength before it reads the body.
func (u *Updater) download(ctx context.Context, name, url string, limit int64, start func(size int64)) ([]byte, error) {
	c := u.client()
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	resp, err := c.downloadClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: server returned %s", name, resp.Status)
	}
	tooLarge := fmt.Errorf("download %s: file is larger than %d bytes", name, limit)
	// A Content-Length over the limit fails before the body arrives.
	if resp.ContentLength > limit {
		return nil, tooLarge
	}
	if start != nil {
		start(resp.ContentLength)
	}
	// The limit stops the read also when the server sends no Content-Length, or a wrong one.
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if int64(len(body)) > limit {
		return nil, tooLarge
	}
	return body, nil
}

// downloadLine gives the progress line for an archive of size bytes.
// A size of -1 means that the server sent no Content-Length.
func downloadLine(asset string, size int64) string {
	if size < 0 {
		return "downloading " + asset + "\n"
	}
	mb := max(1, (size+512<<10)>>20)
	return fmt.Sprintf("downloading %s (%d MB)\n", asset, mb)
}

// runVersion runs "<candidate> version" with only PATH in its environment.
func runVersion(ctx context.Context, candidate string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, candidate, "version")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run %s version: %w", candidate, err)
	}
	return string(out), nil
}
