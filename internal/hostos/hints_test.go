package hostos

import (
	"strings"
	"testing"
)

func TestHintFor(t *testing.T) {
	cases := []struct {
		sys     System
		program string
		want    string
	}{
		{Ubuntu, "chrome", "install Chromium (sudo apt install chromium-browser)"},
		{Debian, "chrome", "install Chromium (sudo apt install chromium)"},
		{MacOS, "chrome", "install Google Chrome (brew install --cask google-chrome)"},
		{Other, "chrome", "install Chrome or Chromium with the package manager of your system"},
		{Ubuntu, "python3", "install python3 (sudo apt install python3)"},
		{MacOS, "python3", "install python3 (xcode-select --install)"},
		{Other, "python3", "install python3 with the package manager of your system"},
		{Debian, "pdftotext", "install poppler-utils (sudo apt install poppler-utils)"},
		{MacOS, "pdftotext", "install poppler (brew install poppler)"},
		{Other, "pdftotext", "install poppler with the package manager of your system"},
		{Ubuntu, "git", "install git (sudo apt install git)"},
		{MacOS, "git", "install git (xcode-select --install)"},
		{Debian, "ffmpeg", "install ffmpeg (sudo apt install ffmpeg)"},
		{MacOS, "ffmpeg", "install ffmpeg (brew install ffmpeg)"},
		{Ubuntu, "whisper-cli", "build whisper-cli from whisper.cpp and put it on PATH"},
		{Other, "whisper-cli", "build whisper-cli from whisper.cpp and put it on PATH"},
		{MacOS, "whisper-cli", "install whisper-cpp (brew install whisper-cpp)"},
		{Other, "edge-tts", "install edge-tts (pipx install edge-tts)"},
		{Ubuntu, "mytool", "install mytool"},
	}
	for _, tc := range cases {
		if got := HintFor(tc.sys, tc.program); got != tc.want {
			t.Errorf("HintFor(%s, %s) = %q, want %q", tc.sys, tc.program, got, tc.want)
		}
	}
}

func TestNoHintCombinesSystems(t *testing.T) {
	programs := []string{"chrome", "python3", "pdftotext", "git", "ffmpeg", "whisper-cli", "edge-tts"}
	for _, sys := range []System{Ubuntu, Debian, MacOS, Other} {
		for _, p := range programs {
			h := HintFor(sys, p)
			if strings.Contains(h, "; ") || (strings.Contains(h, "apt") && strings.Contains(h, "brew")) {
				t.Errorf("HintFor(%s, %s) = %q combines systems", sys, p, h)
			}
		}
	}
}

func TestInstallUnknownProgram(t *testing.T) {
	if name, command := Install(Ubuntu, "mytool"); name != "" || command != "" {
		t.Fatalf("Install(mytool) = %q, %q, want empty", name, command)
	}
	if name, command := Install(Ubuntu, "whisper-cli"); name == "" || command != "" {
		t.Fatalf("Install(whisper-cli) = %q, %q, want a name and no command", name, command)
	}
}

func TestHintUsesCurrent(t *testing.T) {
	t.Cleanup(Set(Debian))
	if got := Hint("chrome"); got != "install Chromium (sudo apt install chromium)" {
		t.Fatalf("Hint(chrome) = %q", got)
	}
}
