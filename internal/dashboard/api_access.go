package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// AccessView is the pairing state of the Access page.
type AccessView struct {
	PairingOn bool         `json:"pairing_on"` // false when Telegram is off
	Pending   []PairingRow `json:"pending"`
	Approved  []PairingRow `json:"approved"`
	TriesLeft int          `json:"tries_left"` // failed approvals left before the lockout; 0 while locked
}

// AccessAPI serves the Access page and the Getting started page.
type AccessAPI struct {
	pairing PairingFuncs
	setup   func(ctx context.Context) []SetupStep
}

// NewAccessAPI returns the routes of the Access page and the Getting started page.
func NewAccessAPI(p PairingFuncs, setup func(ctx context.Context) []SetupStep) *AccessAPI {
	return &AccessAPI{pairing: p, setup: setup}
}

// Register adds the pairing and setup routes to mux.
func (a *AccessAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pairing", a.list)
	mux.HandleFunc("POST /api/pairing/approve", a.approve)
	mux.HandleFunc("POST /api/pairing/revoke", a.revoke)
	mux.HandleFunc("GET /api/setup", a.setupSteps)
}

// accessBodyMax is the size limit of the body of an approve or a revoke request.
const accessBodyMax = 1 << 10

const accessOffText = "Pairing needs Telegram. Set telegram.enabled: true and restart the gateway."

func accessJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func accessErr(w http.ResponseWriter, status int, msg string) {
	accessJSON(w, status, map[string]string{"error": msg})
}

// view reads the pairing rows and splits them by status.
func (a *AccessAPI) view(ctx context.Context) (AccessView, error) {
	v := AccessView{Pending: []PairingRow{}, Approved: []PairingRow{}}
	if a.pairing.List == nil {
		return v, nil
	}
	v.PairingOn = true
	if a.pairing.TriesLeft != nil {
		n, err := a.pairing.TriesLeft(ctx)
		if err != nil {
			return v, err
		}
		v.TriesLeft = n
	}
	rows, err := a.pairing.List(ctx)
	if err != nil {
		return v, err
	}
	for _, r := range rows {
		switch r.Status {
		case "pending":
			v.Pending = append(v.Pending, r)
		case "approved":
			v.Approved = append(v.Approved, r)
		}
	}
	return v, nil
}

func (a *AccessAPI) sendView(w http.ResponseWriter, r *http.Request) {
	v, err := a.view(r.Context())
	if err != nil {
		accessErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	accessJSON(w, http.StatusOK, v)
}

func (a *AccessAPI) list(w http.ResponseWriter, r *http.Request) { a.sendView(w, r) }

func (a *AccessAPI) approve(w http.ResponseWriter, r *http.Request) {
	if a.pairing.Approve == nil {
		accessErr(w, http.StatusConflict, accessOffText)
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, accessBodyMax)).Decode(&body); err != nil {
		accessErr(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		accessErr(w, http.StatusBadRequest, "Type the pairing code.")
		return
	}
	if err := a.pairing.Approve(r.Context(), code); err != nil {
		accessErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.sendView(w, r)
}

func (a *AccessAPI) revoke(w http.ResponseWriter, r *http.Request) {
	if a.pairing.Revoke == nil {
		accessErr(w, http.StatusConflict, accessOffText)
		return
	}
	var body struct {
		Platform string `json:"platform"`
		UserID   string `json:"user_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, accessBodyMax)).Decode(&body); err != nil {
		accessErr(w, http.StatusBadRequest, "The request body is not valid JSON.")
		return
	}
	platform, userID := strings.TrimSpace(body.Platform), strings.TrimSpace(body.UserID)
	if platform == "" || userID == "" {
		accessErr(w, http.StatusBadRequest, "Give the platform and the user id.")
		return
	}
	if err := a.pairing.Revoke(r.Context(), platform, userID); err != nil {
		accessErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.sendView(w, r)
}

func (a *AccessAPI) setupSteps(w http.ResponseWriter, r *http.Request) {
	steps := []SetupStep{}
	if a.setup != nil {
		if got := a.setup(r.Context()); got != nil {
			steps = got
		}
	}
	accessJSON(w, http.StatusOK, steps)
}

// SetupStep is registered in deps.go, so only AccessView is registered here.
func init() { registerAPI(AccessView{}) }
