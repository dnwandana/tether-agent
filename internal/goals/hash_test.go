package goals

import "testing"

func TestItemHash(t *testing.T) {
	for text, want := range map[string]string{
		"check the logs": "50d49ae8",
		"run go vet":     "a699785a",
		"make test":      "22cc66aa",
		"go test ./...":  "1bb497e3",
		"":               "e3b0c442",
	} {
		if got := ItemHash(text); got != want {
			t.Errorf("ItemHash(%q) = %q, want %q", text, got, want)
		}
	}
}
