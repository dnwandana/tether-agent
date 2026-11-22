package dashboard

import "sync"

// TurnState is the snapshot of the running turn of the dashboard key. The key has at most one
// running turn. It is safe for concurrent use.
type TurnState struct {
	mu      sync.Mutex
	running bool
	view    TurnView
	queue   []QueueItem
}

// NewTurnState returns a turn state with no running turn.
func NewTurnState() *TurnState { return &TurnState{} }

// View returns a deep copy of the running turn with the queue, or nil when no turn runs.
func (t *TurnState) View() *TurnView {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return nil
	}
	v := t.view
	v.Running = true
	v.Tools = append([]ToolView{}, t.view.Tools...)
	v.Plan = append([]PlanItem{}, t.view.Plan...)
	v.Queue = append([]QueueItem{}, t.queue...)
	return &v
}

// SetQueue sets the items that wait for the key. View shows them while a turn runs.
func (t *TurnState) SetQueue(q []QueueItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queue = append([]QueueItem(nil), q...)
}

// begin starts a turn with an empty view.
func (t *TurnState) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = true
	t.view = TurnView{}
}

// setText sets the redacted text of the open segment.
func (t *TurnState) setText(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.view.Text = s
}

// newSegment clears the text and the tools. The plan stays.
func (t *TurnState) newSegment() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.view.Text = ""
	t.view.Tools = nil
}

// addTool adds a tool line.
func (t *TurnState) addTool(v ToolView) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.view.Tools = append(t.view.Tools, v)
}

// setTool replaces the tool line with the same call id.
func (t *TurnState) setTool(v ToolView) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.view.Tools {
		if t.view.Tools[i].Call == v.Call {
			t.view.Tools[i] = v
			return
		}
	}
}

// setPlan sets the plan of the turn.
func (t *TurnState) setPlan(items []PlanItem) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.view.Plan = append([]PlanItem(nil), items...)
}

// end ends the turn.
func (t *TurnState) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running = false
	t.view = TurnView{}
}
