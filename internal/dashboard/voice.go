package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// voiceTimeout limits one ffmpeg conversion.
const voiceTimeout = 60 * time.Second

// voiceExt returns the extension of a recorded voice note from its first bytes: ".ogg" for
// "OggS", ".webm" for the EBML magic 1A 45 DF A3, ".m4a" for "ftyp" at offset 4, else "".
// Firefox records Ogg/Opus, Chrome WebM/Opus, and Safari MP4/AAC.
func voiceExt(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("OggS")):
		return ".ogg"
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return ".webm"
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
		return ".m4a"
	}
	return ""
}

// convertVoice converts in to Ogg/Opus, the format of a Telegram voice note, with ffmpeg. It
// runs `ffmpeg -nostdin -y -loglevel error -i <in> -c:a libopus -b:a 32k <out>`, where out is
// in with the extension .ogg, and removes in after a good conversion. A failure removes out,
// keeps in, and returns "" and the error with the stderr text.
func convertVoice(ctx context.Context, ffmpeg, in string) (string, error) {
	out := strings.TrimSuffix(in, filepath.Ext(in)) + ".ogg"
	ctx, cancel := context.WithTimeout(ctx, voiceTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-y", "-loglevel", "error", "-i", in,
		"-c:a", "libopus", "-b:a", "32k", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		return "", fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	_ = os.Remove(in)
	return out, nil
}
