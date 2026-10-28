package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckEntryName(t *testing.T) {
	ok := []string{"state.db", "config.yaml", "memory/", "memory/memory.md", "memory/user_profile.md",
		"skills/", "skills/web/SKILL.md", "scripts/", "scripts/sub/run.sh", "manifest.json"}
	for _, n := range ok {
		if _, err := checkEntryName(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	outside := []string{"../x", "/etc/passwd", "skills/../../x", "skills/./a", "./state.db", "skills//a", ""}
	for _, n := range outside {
		_, err := checkEntryName(n)
		if err == nil || !strings.Contains(err.Error(), "is outside the data folder. The archive is not safe") {
			t.Errorf("%q: %v", n, err)
		}
	}
	notData := []string{".env", ".history", "memory/.memory.lock", "logs/tether.log", "backups/x.tar.gz", "state.db-wal", "skillsx/a"}
	for _, n := range notData {
		_, err := checkEntryName(n)
		if err == nil || !strings.Contains(err.Error(), "is not a tether data file. The archive is not safe") {
			t.Errorf("%q: %v", n, err)
		}
	}
}

func TestCheckEntryNameStripsFolderSlash(t *testing.T) {
	clean, err := checkEntryName("skills/")
	if err != nil || clean != "skills" {
		t.Fatalf("clean=%q err=%v", clean, err)
	}
}

func TestManifestJSONKeys(t *testing.T) {
	b, _ := json.Marshal(Manifest{Format: FormatVersion, Redacted: Redacted{ConfigKeys: []string{"providers.openai.api_key"}}})
	for _, k := range []string{`"format":1`, `"tether_version"`, `"created_at"`, `"schema_version"`, `"files"`,
		`"config_keys"`, `"config_warnings"`, `"state_db"`} {
		if !bytes.Contains(b, []byte(k)) {
			t.Error("missing", k)
		}
	}
	if modeString(0o4755) != "0755" {
		t.Fatal(modeString(0o4755))
	}
}
