package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"sync"
)

// ErrTaskBusy tells that a task of the same kind runs now.
//
//lint:ignore ST1005 The text is a full sentence for the page.
var ErrTaskBusy = errors.New("A task of this kind runs now. Wait for its result.")

// Tasks runs the long actions of the pages on the work context and reports them as task
// events. One task of each kind runs at a time.
type Tasks struct {
	mu       sync.Mutex
	ctx      func() context.Context
	publish  func(Event)       // h.Publish; a test replaces it
	running  map[string]string // kind -> task id
	views    map[string]TaskView
	finished []string // ids of finished tasks, oldest first, at most 20
	started  []string // ids of the stored tasks, in start order
}

// maxFinishedTasks is the number of finished tasks that Get remembers.
const maxFinishedTasks = 20

// NewTasks returns a Tasks that publishes on h and runs on the context of ctx.
func NewTasks(h *Hub, ctx func() context.Context) *Tasks {
	return &Tasks{
		ctx:     ctx,
		publish: h.Publish,
		running: map[string]string{},
		views:   map[string]TaskView{},
	}
}

// TaskOpts names a task: its kind (one task of a kind runs at a time) and its subject.
type TaskOpts struct{ Kind, Subject string }

// TaskHandle changes the view of a running task. Each call stores the view and sends a task event.
type TaskHandle interface {
	Progress(text string)
	Waiting(on bool)
	Code(code string)
	Backup(r BackupReport)
}

// taskHandle is the TaskHandle of one task.
type taskHandle struct {
	t  *Tasks
	id string
}

// change applies fn to the stored view of a running task and publishes the result.
func (h *taskHandle) change(fn func(v *TaskView)) {
	h.t.mu.Lock()
	v, ok := h.t.views[h.id]
	if !ok || v.State != "running" {
		h.t.mu.Unlock()
		return
	}
	fn(&v)
	h.t.views[h.id] = v
	h.t.mu.Unlock()
	h.t.publish(Event{Name: "task", Data: v})
}

func (h *taskHandle) Progress(text string) { h.change(func(v *TaskView) { v.Text = text }) }
func (h *taskHandle) Waiting(on bool)      { h.change(func(v *TaskView) { v.Waiting = on }) }
func (h *taskHandle) Code(code string)     { h.change(func(v *TaskView) { v.Code = code }) }
func (h *taskHandle) Backup(r BackupReport) {
	h.change(func(v *TaskView) { v.Backup = &r })
}

// Start runs one task of the kind. It returns the error of the work context when that context
// has ended, and ErrTaskBusy when a task of the same kind runs.
func (t *Tasks) Start(kind string, run func(ctx context.Context, progress func(text string)) (string, error)) (id string, err error) {
	return t.StartWith(TaskOpts{Kind: kind}, func(ctx context.Context, h TaskHandle) (string, error) {
		return run(ctx, h.Progress)
	})
}

// StartWith is Start with a subject and a handle. The handle sets the progress text, the
// waiting flag, the refusal code, and the backup report of the task.
func (t *Tasks) StartWith(o TaskOpts, run func(ctx context.Context, h TaskHandle) (string, error)) (id string, err error) {
	t.mu.Lock()
	work := t.ctx()
	if err := work.Err(); err != nil {
		t.mu.Unlock()
		return "", err
	}
	if _, busy := t.running[o.Kind]; busy {
		t.mu.Unlock()
		return "", ErrTaskBusy
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.mu.Unlock()
		return "", err
	}
	id = hex.EncodeToString(raw[:])
	view := TaskView{ID: id, Kind: o.Kind, Subject: o.Subject, State: "running"}
	t.running[o.Kind] = id
	t.views[id] = view
	t.started = append(t.started, id)
	t.mu.Unlock()
	t.publish(Event{Name: "task", Data: view})

	go t.run(work, view, run)
	return id, nil
}

// run runs one task and stores and publishes its last view.
func (t *Tasks) run(work context.Context, view TaskView, run func(context.Context, TaskHandle) (string, error)) {
	ctx, cancel := context.WithCancel(work)
	defer cancel()
	var state, text string
	func() {
		defer func() {
			if recover() != nil {
				state, text = "error", "The task stopped with an internal error."
			}
		}()
		out, err := run(ctx, &taskHandle{t: t, id: view.ID})
		if err != nil {
			state, text = "error", err.Error()
			return
		}
		state, text = "done", out
	}()
	t.mu.Lock()
	final := t.views[view.ID]
	final.State, final.Text, final.Waiting = state, text, false
	t.views[final.ID] = final
	delete(t.running, final.Kind)
	t.finished = append(t.finished, final.ID)
	if len(t.finished) > maxFinishedTasks {
		old := t.finished[0]
		delete(t.views, old)
		t.finished = t.finished[1:]
		for i, id := range t.started {
			if id == old {
				t.started = append(t.started[:i], t.started[i+1:]...)
				break
			}
		}
	}
	t.mu.Unlock()
	t.publish(Event{Name: "task", Data: final})
}

// List returns the stored tasks of the kind, newest first. An empty kind returns every kind.
// It never returns nil.
func (t *Tasks) List(kind string) []TaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := []TaskView{}
	for _, id := range slices.Backward(t.started) {
		if v, ok := t.views[id]; ok && (kind == "" || v.Kind == kind) {
			out = append(out, v)
		}
	}
	return out
}

// Get returns the last view of a running task or of one of the last 20 finished tasks.
func (t *Tasks) Get(id string) (TaskView, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.views[id]
	return v, ok
}

// tasksAPI serves the state of one task.
type tasksAPI struct{ t *Tasks }

// NewTasksAPI returns the routes GET /api/tasks and GET /api/tasks/{id}. The first lists the
// tasks of the kind in the query, newest first. The second gives the last TaskView of a task,
// and 404 for an unknown id.
func NewTasksAPI(t *Tasks) Routes { return tasksAPI{t: t} }

// Register adds the task routes to mux.
func (a tasksAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, a.t.List(r.URL.Query().Get("kind")))
	})
	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := a.t.Get(r.PathValue("id"))
		if !ok {
			authError(w, http.StatusNotFound, "No task with this id.")
			return
		}
		writeJSON(w, v)
	})
}
