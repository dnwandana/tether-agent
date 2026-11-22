package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dnwandana/tether-agent/internal/backup"
)

// OpsOptions holds what the routes of the operations page need.
type OpsOptions struct {
	DataDir     string // the data dir; the archives are in <DataDir>/backups
	ConfigPath  string // the config.yaml of the running gateway
	Version     string // goes into the backup manifest
	DotEnv      func() (map[string]string, error)
	Redact      func(string) string
	Tasks       *Tasks
	UpdateCheck func(ctx context.Context) (UpdateInfo, error)
	UpdateRun   func(ctx context.Context) error
}

// OpsAPI serves the operations page.
type OpsAPI struct{ o OpsOptions }

// ConfigView is the masked config.yaml.
type ConfigView struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

// BackupFile is one archive in the backups folder.
type BackupFile struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// BackupView lists the archives, newest first.
type BackupView struct {
	Dir   string       `json:"dir"`
	Files []BackupFile `json:"files"`
}

func init() { registerAPI(ConfigView{}, BackupView{}, BackupFile{}) }

// NewOpsAPI returns the routes of the operations page.
func NewOpsAPI(o OpsOptions) *OpsAPI { return &OpsAPI{o: o} }

// Register adds the routes to mux.
func (a *OpsAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", a.config)
	mux.HandleFunc("GET /api/backup", a.backups)
	mux.HandleFunc("POST /api/backup", a.backupStart)
	mux.HandleFunc("GET /api/backup/{file}", a.download)
	mux.HandleFunc("GET /api/update", a.updateInfo)
	mux.HandleFunc("POST /api/update", a.updateStart)
}

func opsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func opsErr(w http.ResponseWriter, status int, msg string) {
	opsJSON(w, status, map[string]string{"error": msg})
}

func (a *OpsAPI) backupDir() string { return filepath.Join(a.o.DataDir, "backups") }

func (a *OpsAPI) config(w http.ResponseWriter, r *http.Request) {
	raw, err := os.ReadFile(a.o.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		opsErr(w, http.StatusNotFound, "No config.yaml in the data dir.")
		return
	}
	if err != nil {
		opsErr(w, http.StatusInternalServerError, "Cannot read config.yaml.")
		return
	}
	masked, err := backup.MaskConfig(raw)
	if err != nil {
		opsErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	text := string(masked)
	if a.o.Redact != nil {
		text = a.o.Redact(text)
	}
	opsJSON(w, http.StatusOK, ConfigView{Path: a.o.ConfigPath, Text: text})
}

func (a *OpsAPI) backups(w http.ResponseWriter, r *http.Request) {
	v := BackupView{Dir: a.backupDir(), Files: []BackupFile{}}
	entries, _ := os.ReadDir(v.Dir)
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "tether-backup-") || !strings.HasSuffix(n, ".tar.gz") {
			continue
		}
		fi, err := os.Lstat(filepath.Join(v.Dir, n))
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		v.Files = append(v.Files, BackupFile{Name: n, Size: fi.Size(), Modified: fi.ModTime()})
	}
	slices.SortFunc(v.Files, func(x, y BackupFile) int {
		if c := y.Modified.Compare(x.Modified); c != 0 {
			return c
		}
		return strings.Compare(y.Name, x.Name)
	})
	if len(v.Files) > 20 {
		v.Files = v.Files[:20]
	}
	opsJSON(w, http.StatusOK, v)
}

func (a *OpsAPI) backupStart(w http.ResponseWriter, r *http.Request) {
	id, err := a.o.Tasks.Start("backup", func(ctx context.Context, progress func(string)) (string, error) {
		env := map[string]string{}
		if a.o.DotEnv != nil {
			e, err := a.o.DotEnv()
			if err != nil {
				return "", a.redactErr(err)
			}
			if e != nil {
				env = e
			}
		}
		res, err := backup.Create(ctx, backup.Options{DataDir: a.o.DataDir, DotEnv: env,
			Environ: os.Environ(), Version: a.o.Version, Now: time.Now})
		if err != nil {
			return "", a.redactErr(err)
		}
		return "Saved " + filepath.Base(res.Path) + " (" + strconv.FormatInt(res.Size, 10) + " bytes).", nil
	})
	opsStarted(w, id, err)
}

// redactErr returns err with the secrets masked in its text. A nil Redact returns err.
func (a *OpsAPI) redactErr(err error) error {
	if a.o.Redact == nil {
		return err
	}
	return errors.New(a.o.Redact(err.Error()))
}

func opsStarted(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, ErrTaskBusy) {
		opsErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		opsErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	opsJSON(w, http.StatusAccepted, map[string]string{"task_id": id})
}

func (a *OpsAPI) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".tar.gz") {
		opsErr(w, http.StatusNotFound, "No such backup.")
		return
	}
	path := filepath.Join(a.backupDir(), name)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		opsErr(w, http.StatusNotFound, "No such backup.")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		opsErr(w, http.StatusNotFound, "No such backup.")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (a *OpsAPI) updateInfo(w http.ResponseWriter, r *http.Request) {
	if a.o.UpdateCheck == nil {
		opsErr(w, http.StatusBadGateway, "The update check is not available.")
		return
	}
	info, err := a.o.UpdateCheck(r.Context())
	if err != nil {
		opsErr(w, http.StatusBadGateway, a.redactErr(err).Error())
		return
	}
	opsJSON(w, http.StatusOK, info)
}

func (a *OpsAPI) updateStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || !body.Confirm {
		opsErr(w, http.StatusBadRequest, "Confirm the update.")
		return
	}
	if a.o.UpdateCheck == nil || a.o.UpdateRun == nil {
		opsErr(w, http.StatusBadGateway, "The update is not available.")
		return
	}
	info, err := a.o.UpdateCheck(r.Context())
	if err != nil {
		opsErr(w, http.StatusBadGateway, a.redactErr(err).Error())
		return
	}
	if !info.Writable {
		opsErr(w, http.StatusForbidden, "The binary at "+info.Path+
			" is not writable by this user. Run `tether update` as a user who can write it.")
		return
	}
	id, err := a.o.Tasks.Start("update", func(ctx context.Context, progress func(string)) (string, error) {
		if err := a.o.UpdateRun(ctx); err != nil {
			return "", a.redactErr(err)
		}
		return "Update installed. The gateway restarts.", nil
	})
	opsStarted(w, id, err)
}
