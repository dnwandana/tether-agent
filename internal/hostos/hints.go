package hostos

// install is the package and the install command of a program on one system.
type install struct {
	name    string
	command string
}

// program holds the install data of one program. name is the package name
// for a system that has no entry in bySystem.
type program struct {
	name     string
	bySystem map[System]install
}

// apt gives the same install entry for Ubuntu and Debian.
func apt(name string) map[System]install {
	in := install{name, "sudo apt install " + name}
	return map[System]install{Ubuntu: in, Debian: in}
}

// with adds the entry for sys to m and returns m.
func with(m map[System]install, sys System, in install) map[System]install {
	m[sys] = in
	return m
}

// programs is the one table of install commands. A Linux command starts with
// sudo. A brew or pipx command does not, because it installs for the user.
var programs = map[string]program{
	"chrome": {"Chrome or Chromium", map[System]install{
		// Ubuntu has no chromium deb. The chromium-browser package installs the snap.
		Ubuntu: {"Chromium", "sudo apt install chromium-browser"},
		Debian: {"Chromium", "sudo apt install chromium"},
		// Homebrew disabled the chromium cask, so macOS gets Google Chrome.
		MacOS: {"Google Chrome", "brew install --cask google-chrome"},
	}},
	"python3":   {"python3", with(apt("python3"), MacOS, install{"python3", "xcode-select --install"})},
	"pdftotext": {"poppler", with(apt("poppler-utils"), MacOS, install{"poppler", "brew install poppler"})},
	"git":       {"git", with(apt("git"), MacOS, install{"git", "xcode-select --install"})},
	"ffmpeg":    {"ffmpeg", with(apt("ffmpeg"), MacOS, install{"ffmpeg", "brew install ffmpeg"})},
	// whisper-cli has no Linux package, so only macOS has a command.
	"whisper-cli": {"whisper-cli", map[System]install{
		MacOS: {"whisper-cpp", "brew install whisper-cpp"},
	}},
	"edge-tts": {"edge-tts", map[System]install{
		Ubuntu: {"edge-tts", "pipx install edge-tts"},
		Debian: {"edge-tts", "pipx install edge-tts"},
		MacOS:  {"edge-tts", "pipx install edge-tts"},
		Other:  {"edge-tts", "pipx install edge-tts"},
	}},
}

// Install returns the package name and the install command of prog on s.
// The command is empty when s has no command. Both are empty for a program
// that is not in the table.
func Install(s System, prog string) (name, command string) {
	p, ok := programs[prog]
	if !ok {
		return "", ""
	}
	if in, ok := p.bySystem[s]; ok {
		return in.name, in.command
	}
	return p.name, ""
}

// HintFor returns the install hint for the program prog on s. The hint gives the
// command of s only, never the commands of two systems.
func HintFor(s System, prog string) string {
	name, command := Install(s, prog)
	switch {
	case name == "":
		return "install " + prog
	case command != "":
		return "install " + name + " (" + command + ")"
	case prog == "whisper-cli":
		return "build whisper-cli from whisper.cpp and put it on PATH"
	default:
		return "install " + name + " with the package manager of your system"
	}
}

// Hint returns the install hint for the program prog on the system of this host.
func Hint(prog string) string {
	return HintFor(Current(), prog)
}
