package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
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

// Start runs one task of the kind. It returns the error of the work context when that context
// has ended, and ErrTaskBusy when a task of the same kind runs.
func (t *Tasks) Start(kind string, run func(ctx context.Context, progress func(text string)) (string, error)) (id string, err error) {
	t.mu.Lock()
	work := t.ctx()
	if err := work.Err(); err != nil {
		t.mu.Unlock()
		return "", err
	}
	if _, busy := t.running[kind]; busy {
		t.mu.Unlock()
		return "", ErrTaskBusy
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.mu.Unlock()
		return "", err
	}
	id = hex.EncodeToString(raw[:])
	view := TaskView{ID: id, Kind: kind, State: "running"}
	t.running[kind] = id
	t.views[id] = view
	t.mu.Unlock()
	t.publish(Event{Name: "task", Data: view})

	go t.run(work, view, run)
	return id, nil
}

// run runs one task and stores and publishes its last view.
func (t *Tasks) run(work context.Context, view TaskView, run func(context.Context, func(string)) (string, error)) {
	ctx, cancel := context.WithCancel(work)
	defer cancel()
	progress := func(text string) {
		v := view
		v.Text = text
		t.mu.Lock()
		t.views[v.ID] = v
		t.mu.Unlock()
		t.publish(Event{Name: "task", Data: v})
	}
	final := view
	func() {
		defer func() {
			if recover() != nil {
				final.State, final.Text = "error", "The task stopped with an internal error."
			}
		}()
		text, err := run(ctx, progress)
		if err != nil {
			final.State, final.Text = "error", err.Error()
			return
		}
		final.State, final.Text = "done", text
	}()
	t.mu.Lock()
	t.views[final.ID] = final
	delete(t.running, final.Kind)
	t.finished = append(t.finished, final.ID)
	if len(t.finished) > maxFinishedTasks {
		delete(t.views, t.finished[0])
		t.finished = t.finished[1:]
	}
	t.mu.Unlock()
	t.publish(Event{Name: "task", Data: final})
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

// NewTasksAPI returns the route GET /api/tasks/{id}. It gives the last TaskView of a task,
// and 404 for an unknown id.
func NewTasksAPI(t *Tasks) Routes { return tasksAPI{t: t} }

// Register adds the task route to mux.
func (a tasksAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, ok := a.t.Get(r.PathValue("id"))
		if !ok {
			authError(w, http.StatusNotFound, "No task with this id.")
			return
		}
		writeJSON(w, v)
	})
}
