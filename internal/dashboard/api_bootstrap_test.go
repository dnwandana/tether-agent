package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBootstrapView(t *testing.T) {
	r := NewBootstrap(BootstrapDeps{
		Commands:    func() []CommandRow { return []CommandRow{{Name: "/usage", Description: "Show usage"}} },
		Skills:      func() []CommandRow { return []CommandRow{{Name: "/release-notes", Description: "Write notes"}} },
		Version:     "v0.24.0",
		MaxUploadMB: 20,
		TLS:         true,
		DrainS:      180,
		Toolsets:    []string{"terminal", "web"},
	})
	mux := http.NewServeMux()
	r.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/bootstrap", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var v BootstrapView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Commands) != 1 || v.Commands[0].Name != "/usage" || len(v.Skills) != 1 ||
		v.Version != "v0.24.0" || v.MaxUploadMB != 20 || !v.TLS || v.BuildID != BuildID() ||
		v.DrainS != 180 || len(v.Toolsets) != 2 || v.Toolsets[1] != "web" {
		t.Fatalf("%+v", v)
	}
	name, off := time.Now().Zone()
	if v.HostZone != name || v.HostOffsetMin != off/60 {
		t.Fatalf("host zone %q %d, want %q %d", v.HostZone, v.HostOffsetMin, name, off/60)
	}
}

func TestBootstrapNilListsAreEmptyArrays(t *testing.T) {
	mux := http.NewServeMux()
	NewBootstrap(BootstrapDeps{}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/bootstrap", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["commands"]) != "[]" || string(raw["skills"]) != "[]" || string(raw["toolsets"]) != "[]" {
		t.Fatalf("%s", rec.Body.String())
	}
}
