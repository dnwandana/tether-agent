package gateway

import (
	"strings"
	"testing"
)

func TestFormatContext(t *testing.T) {
	for in, want := range map[int]string{
		200000:  "200K tokens",
		1000000: "1.0M tokens",
		1048576: "1.0M tokens",
		512:     "512 tokens",
		1500:    "1K tokens",
	} {
		if got := formatContext(in); got != want {
			t.Errorf("formatContext(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTipsAreCopiedFromSpec(t *testing.T) {
	if len(Tips) != 20 {
		t.Fatal(len(Tips))
	}
	if Tips[0] != "/fork copies this session, so you can try a different idea and keep the original." {
		t.Fatal(Tips[0])
	}
	if Tips[8] != "/archive hides a session from the list. /sessions archived shows the hidden sessions again." {
		t.Fatal(Tips[8])
	}
	if Tips[19] != "Run `tether update` in a terminal to install the newest release." {
		t.Fatal(Tips[19])
	}
	for _, tip := range Tips {
		if strings.Contains(tip, "/resume brings it back") {
			t.Fatal(tip)
		}
	}
	for range 50 {
		if tip := pickTip(); tip == "" {
			t.Fatal("empty tip")
		}
	}
}
