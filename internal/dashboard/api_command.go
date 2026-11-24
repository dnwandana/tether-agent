package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dnwandana/tether-agent/internal/agent"
	"github.com/dnwandana/tether-agent/internal/gateway"
	"github.com/dnwandana/tether-agent/internal/tools"
)

// firstReplyWait is the longest time that POST /api/command waits for the first reply.
// Tests change it.
var firstReplyWait = 2 * time.Second

const (
	cmdBodyMax      = 64 << 10
	textNotBuiltIn  = "This route takes only a built-in command. Send other text to /api/messages."
	textCardClosed  = "This card is already answered."
	textBulkAnswers = "Bulk answers are deny or once."
	textBadCmdBody  = "The request body is not valid."
)

// CommandRequest is the body of POST /api/command.
type CommandRequest struct {
	Text   string `json:"text"`
	Button int    `json:"button"`
}

// Accepted is the 202 body of POST /api/command when no reply came in time.
type Accepted struct {
	RequestID string `json:"request_id"`
}

// BulkRequest is the body of POST /api/cards/bulk.
type BulkRequest struct {
	Answer string   `json:"answer"`
	IDs    []string `json:"ids"`
}

// BulkResult is the answer of POST /api/cards/bulk.
type BulkResult struct {
	Answered int `json:"answered"`
}

// CardsView is the body of GET /api/cards. A page reads it after a 409.
type CardsView struct {
	Open     []Card         `json:"open"`
	Answered []AnsweredCard `json:"answered"`
}

func init() { registerAPI(CommandRequest{}, Accepted{}, BulkRequest{}, BulkResult{}, CardsView{}) }

type commandAPI struct {
	ch     *Channel
	redact func(string) string
}

// NewCommandAPI returns the command route and the card routes of ch. redact masks the
// reply text of the HTTP answer.
func NewCommandAPI(ch *Channel, redact func(string) string) Routes {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	return &commandAPI{ch: ch, redact: redact}
}

// Register adds POST /api/command, GET /api/cards, POST /api/cards/bulk, and POST /api/cards/{id}.
func (a *commandAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cards", a.cards)
	mux.HandleFunc("POST /api/command", a.command)
	mux.HandleFunc("POST /api/cards/bulk", a.bulk)
	mux.HandleFunc("POST /api/cards/{id}", a.card)
}

// cmdJSON writes v with the status code.
func cmdJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// cmdDecode decodes the JSON body into v. An empty body leaves v as it is.
func cmdDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, cmdBodyMax)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		authError(w, http.StatusBadRequest, textBadCmdBody)
		return false
	}
	return true
}

// cards sends the open cards and the answered-card ring. Both lists are never nil.
func (a *commandAPI) cards(w http.ResponseWriter, r *http.Request) {
	answered := a.ch.hub.Answered()
	if answered == nil {
		answered = []AnsweredCard{}
	}
	cmdJSON(w, http.StatusOK, CardsView{Open: a.ch.Cards().Open(), Answered: answered})
}

func (a *commandAPI) command(w http.ResponseWriter, r *http.Request) {
	ch := a.ch
	if ch.Stopping() {
		authError(w, http.StatusServiceUnavailable, textStopping)
		return
	}
	var req CommandRequest
	if !cmdDecode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	id, err := randHex(8)
	if err != nil {
		authError(w, http.StatusInternalServerError, textBadCmdBody)
		return
	}
	tap := &replyTap{first: make(chan gateway.Reply, 1)}
	if reply, ok := ch.Cards().Local(text); ok {
		tap.wrap(ch.NewRenderer(id)).Ack(reply)
		cmdJSON(w, http.StatusOK, ReplyView{RequestID: id, Text: a.redact(reply)})
		return
	}
	name, _, ok := gateway.IsCommand(text)
	if _, built := gateway.Commands()[name]; !ok || !built {
		authError(w, http.StatusBadRequest, textNotBuiltIn)
		return
	}
	gw := ch.Gateway()
	if gw == nil {
		authError(w, http.StatusServiceUnavailable, textStopping)
		return
	}
	in := ch.Inbound(text)
	in.ButtonMsgID = req.Button
	work := ch.WorkCtx()
	go func() {
		gw.Submit(work, in, func() gateway.Renderer { return tap.wrap(ch.NewRenderer(id)) }, nil)
		// A command can change the active session, its title, or its model.
		ch.refreshSession(context.Background())
	}()
	timer := time.NewTimer(firstReplyWait)
	defer timer.Stop()
	select {
	case rep := <-tap.first:
		buttons := make([][]Button, 0, len(rep.Buttons))
		for _, row := range rep.Buttons {
			out := make([]Button, 0, len(row))
			for _, b := range row {
				out = append(out, Button{Text: b.Text, Data: b.Data})
			}
			buttons = append(buttons, out)
		}
		cmdJSON(w, http.StatusOK, ReplyView{RequestID: id, Text: a.redact(rep.Text), Buttons: buttons, Edit: rep.Edit})
	case <-timer.C:
		cmdJSON(w, http.StatusAccepted, Accepted{RequestID: id})
	case <-r.Context().Done():
		cmdJSON(w, http.StatusAccepted, Accepted{RequestID: id})
	}
}

func (a *commandAPI) card(w http.ResponseWriter, r *http.Request) {
	var ans CardAnswer
	if !cmdDecode(w, r, &ans) {
		return
	}
	work := a.ch.WorkCtx()
	if work == nil || work.Err() != nil {
		authError(w, http.StatusServiceUnavailable, textStopping)
		return
	}
	err := a.ch.Cards().Answer(work, r.PathValue("id"), ans)
	switch {
	case errors.Is(err, ErrCardClosed):
		authError(w, http.StatusConflict, textCardClosed)
	case err != nil:
		authError(w, http.StatusBadRequest, err.Error())
	default:
		cmdJSON(w, http.StatusOK, map[string]string{})
	}
}

func (a *commandAPI) bulk(w http.ResponseWriter, r *http.Request) {
	var req BulkRequest
	if !cmdDecode(w, r, &req) {
		return
	}
	if req.Answer != "deny" && req.Answer != "once" {
		authError(w, http.StatusBadRequest, textBulkAnswers)
		return
	}
	work := a.ch.WorkCtx()
	if work == nil || work.Err() != nil {
		authError(w, http.StatusServiceUnavailable, textStopping)
		return
	}
	n, err := a.ch.Cards().Bulk(work, req.IDs, req.Answer)
	if err != nil {
		authError(w, http.StatusBadRequest, err.Error())
		return
	}
	cmdJSON(w, http.StatusOK, BulkResult{Answered: n})
}

// replyTap gives the first command reply of one request to the HTTP handler.
type replyTap struct {
	once  sync.Once
	first chan gateway.Reply // capacity 1
}

func (t *replyTap) give(r gateway.Reply) { t.once.Do(func() { t.first <- r }) }

// wrap returns r with Ack and AckReply caught by the tap.
func (t *replyTap) wrap(r gateway.Renderer) gateway.Renderer {
	return &tapRenderer{Renderer: r, tap: t}
}

// tapRenderer forwards each optional renderer interface to the inner renderer, because the
// gateway checks them by type.
type tapRenderer struct {
	gateway.Renderer
	tap *replyTap
}

func (r *tapRenderer) Ack(text string) {
	r.tap.give(gateway.Reply{Text: text})
	r.Renderer.Ack(text)
}

func (r *tapRenderer) AckReply(rep gateway.Reply) {
	r.tap.give(rep)
	if in, ok := r.Renderer.(gateway.ReplyRenderer); ok {
		in.AckReply(rep)
		return
	}
	r.Renderer.Ack(rep.Text)
}

func (r *tapRenderer) AckReplyHandle(rep gateway.Reply) gateway.AckHandle {
	r.tap.give(rep)
	if in, ok := r.Renderer.(gateway.HandleRenderer); ok {
		return in.AckReplyHandle(rep)
	}
	r.Renderer.Ack(rep.Text)
	return nopAckHandle{}
}

type nopAckHandle struct{}

func (nopAckHandle) MsgID() int { return 0 }
func (nopAckHandle) Close(string) <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func (r *tapRenderer) GoalLine(line string) {
	if in, ok := r.Renderer.(gateway.GoalLineRenderer); ok {
		in.GoalLine(line)
	}
}

func (r *tapRenderer) TurnContext(ctx context.Context) {
	if in, ok := r.Renderer.(gateway.TurnContextRenderer); ok {
		in.TurnContext(ctx)
	}
}

func (r *tapRenderer) SetSession(id string) {
	if in, ok := r.Renderer.(gateway.SessionRenderer); ok {
		in.SetSession(id)
	}
}

func (r *tapRenderer) Plan(before, after []tools.TodoItem) {
	if in, ok := r.Renderer.(agent.PlanSink); ok {
		in.Plan(before, after)
	}
}

func (r *tapRenderer) ToolCall(callID, name string, args json.RawMessage) {
	if in, ok := r.Renderer.(agent.ToolDetailSink); ok {
		in.ToolCall(callID, name, args)
	}
}

func (r *tapRenderer) ToolResult(callID, text string, isError bool, d time.Duration) {
	if in, ok := r.Renderer.(agent.ToolDetailSink); ok {
		in.ToolResult(callID, text, isError, d)
	}
}
