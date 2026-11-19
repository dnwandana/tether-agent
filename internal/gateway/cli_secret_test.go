package gateway

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dnwandana/tether-agent/internal/tools"
)

// secretChanReader is a chanReader with a masked read. A plain read takes from res, and a
// masked read takes from sec.
type secretChanReader struct {
	chanReader
	sec     chan lineResult
	prompts []string
}

func (r *secretChanReader) ReadSecret(prompt string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	x := <-r.sec
	return x.line, x.err
}

func TestFeedSecretReadUsesMaskedRead(t *testing.T) {
	r := &secretChanReader{chanReader: chanReader{res: make(chan lineResult)}, sec: make(chan lineResult, 1)}
	f := newLineFeed(r, nil)
	defer f.close()
	r.sec <- lineResult{line: "pypi-AgEIcHlwaS5vcmc"}
	v, err := f.secret(context.Background(), "Value for PYPI_TOKEN: ")
	if err != nil || v != "pypi-AgEIcHlwaS5vcmc" {
		t.Fatal(v, err)
	}
	// The masked read writes no history line: the plain ReadLine never ran.
	select {
	case r.res <- lineResult{line: "x"}:
		t.Fatal("a plain read waits")
	default:
	}
}

func TestFeedSecretRefusedWhilePlainReadWaits(t *testing.T) {
	r := &secretChanReader{chanReader: chanReader{res: make(chan lineResult)}, sec: make(chan lineResult, 1)}
	f := newLineFeed(r, nil)
	defer f.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.nextPrompt(ctx, nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := f.secret(context.Background(), "Value for PYPI_TOKEN: "); !errors.Is(err, errFeedBusy) {
		t.Fatal(err)
	}
	// A reader with no masked read refuses too.
	g := newLineFeed(&chanReader{res: make(chan lineResult)}, nil)
	defer g.close()
	if _, err := g.secret(context.Background(), "Value for PYPI_TOKEN: "); !errors.Is(err, errNoMaskedRead) {
		t.Fatal(err)
	}
}

func TestCLIAskSecret(t *testing.T) {
	q := tools.SecretQuestion{Name: "PYPI_TOKEN", Commands: []string{"twine upload", "uv publish"},
		Reason: "publish tether-tools 0.4.1 to PyPI"}
	var out bytes.Buffer
	saved := map[string]string{}
	a := &CLIAsk{Save: func(name, value string) error { saved[name] = value; return nil }}
	value := "pypi-AgEIcHlwaS5vcmc"
	detach := a.attach(&out, nil, func(_ context.Context, prompt string) (string, error) {
		if prompt != "Value for PYPI_TOKEN: " {
			t.Errorf("prompt %q", prompt)
		}
		return "  " + value + "\n", nil
	})
	ok, err := a.AskSecret(context.Background(), q)
	detach()
	if !ok || err != nil || saved["PYPI_TOKEN"] != value {
		t.Fatal(ok, err, saved)
	}
	want := "🔑 Secret needed: PYPI_TOKEN\nFor: twine upload, uv publish\nReason: publish tether-tools 0.4.1 to PyPI\n" +
		"✅ Saved PYPI_TOKEN.\n"
	if out.String() != want || strings.Contains(out.String(), value) {
		t.Fatalf("%q", out.String())
	}
	var _ tools.SecretAsker = a
}

func TestCLIAskSecretRefusals(t *testing.T) {
	q := tools.SecretQuestion{Name: "PYPI_TOKEN", Commands: []string{"twine upload"}}
	busy := "The CLI is busy. Ask the owner to type /secrets set PYPI_TOKEN at the prompt."
	a := &CLIAsk{Save: func(string, string) error { return nil }}
	// No CLI is attached.
	if ok, err := a.AskSecret(context.Background(), q); ok || err == nil || err.Error() != busy {
		t.Fatal(ok, err)
	}
	for _, tc := range []struct {
		read    error
		line    string
		save    error
		wantErr string
	}{
		{read: errFeedBusy, wantErr: busy},
		{read: errNoMaskedRead, wantErr: busy},
		{line: "a\\nb", save: errors.New("x"), wantErr: "I could not save PYPI_TOKEN: x. Send it again."},
	} {
		a.Save = func(string, string) error { return tc.save }
		var out bytes.Buffer
		detach := a.attach(&out, nil, func(context.Context, string) (string, error) { return tc.line, tc.read })
		ok, err := a.AskSecret(context.Background(), q)
		detach()
		if ok || err == nil || err.Error() != tc.wantErr {
			t.Errorf("%+v: %v %v", tc, ok, err)
		}
	}
}

func TestSecretReaderNotices(t *testing.T) {
	const prompt = "Value for PYPI_TOKEN: "
	// A refused read leaves no open prompt line.
	r := &secretChanReader{chanReader: chanReader{res: make(chan lineResult)}, sec: make(chan lineResult, 1)}
	f := newLineFeed(r, nil)
	defer f.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.nextPrompt(ctx, nil, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := &CLI{Out: &out}
	read := c.secretReader(r, f)
	if _, err := read(context.Background(), prompt); !errors.Is(err, errFeedBusy) {
		t.Fatal(err)
	}
	if got := out.String(); got != prompt+"\n" {
		t.Fatalf("busy output %q", got)
	}
	// A stopped read tells the owner that the next line is not a command.
	r2 := &secretChanReader{chanReader: chanReader{res: make(chan lineResult)}, sec: make(chan lineResult, 1)}
	f2 := newLineFeed(r2, nil)
	defer f2.close()
	out.Reset()
	stop, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := c.secretReader(r2, f2)(stop, prompt); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if got := out.String(); !strings.HasSuffix(got, "\nThe secret read stopped. Press Enter to continue.\n") {
		t.Fatalf("stop output %q", got)
	}
}
