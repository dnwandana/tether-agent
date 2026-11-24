package dashboard

import "time"

// Event is one server-sent event. Data becomes the JSON of the data line. A "delta" event has
// the text as a string.
type Event struct {
	Name string
	Data any
}

// Snapshot is the data of the hello event. It shows the state of the session at connect time.
type Snapshot struct {
	Turn          *TurnView      `json:"turn"`
	Cards         []Card         `json:"cards"`
	Status        Status         `json:"status"`
	SessionID     string         `json:"session_id"`
	Session       SessionView    `json:"session"`
	LastMessageID int64          `json:"last_message_id"`
	Notices       []Notice       `json:"notices"`
	Answered      []AnsweredCard `json:"answered"`
	BuildID       string         `json:"build_id"`
}

// SessionView is the header data of the session. It is also the data of the session event.
type SessionView struct {
	ID       string       `json:"id"`
	Title    string       `json:"title"`
	Model    string       `json:"model"`
	Entry    string       `json:"entry"`
	Effort   string       `json:"effort"`
	GoalLine string       `json:"goal_line"`
	Context  *ContextView `json:"context"`
}

// ContextView is the size of the context of the session against its window.
type ContextView struct {
	Tokens    int     `json:"tokens"`
	Window    int     `json:"window"`
	Threshold float64 `json:"threshold"`
}

// TurnView is the state of the running turn.
type TurnView struct {
	Running bool        `json:"running"`
	Text    string      `json:"text"`
	Tools   []ToolView  `json:"tools"`
	Plan    []PlanItem  `json:"plan"`
	Queue   []QueueItem `json:"queue"`
}

// ToolView is one tool line of a turn.
type ToolView struct {
	Call       string `json:"call"`
	Name       string `json:"name"`
	Emoji      string `json:"emoji"`
	Preview    string `json:"preview"`
	Args       string `json:"args"`
	State      string `json:"state"` // running, done, error
	DurationMS int64  `json:"duration_ms"`
	Text       string `json:"text"`
	Cut        bool   `json:"cut"`
}

// PlanItem is one line of the plan of a turn.
type PlanItem struct {
	Text   string `json:"text"`
	State  string `json:"state"`
	Parent int    `json:"parent"`
}

// QueueItem is one message that waits for the running turn.
type QueueItem struct {
	Kind    string `json:"kind"`
	Preview string `json:"preview"`
}

// Card is one open prompt: an approval, a pin, a question, a secret, or a sign-in request.
type Card struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"` // approval, pin, clarify, secret, signin
	Header      string    `json:"header"`
	Summary     string    `json:"summary"`
	Command     string    `json:"command"`
	Rule        string    `json:"rule"`
	Description string    `json:"description"`
	Workdir     string    `json:"workdir"`
	Via         string    `json:"via"`
	Child       string    `json:"child"`
	Diff        string    `json:"diff"`
	Secrets     []string  `json:"secrets"`
	Findings    []string  `json:"findings"`
	HumanOnly   bool      `json:"human_only"`
	Answers     []string  `json:"answers"`
	Choices     []string  `json:"choices"`
	Multi       bool      `json:"multi"`
	Deadline    time.Time `json:"deadline"`
	SecretName  string    `json:"secret_name"`
	Reason      string    `json:"reason"`
	Device      string    `json:"device"`
	IP          string    `json:"ip"`
	Code        string    `json:"code"`
}

// AnsweredCard is the record of one answered card. Header and Preview are redacted and cut at
// 300 characters.
type AnsweredCard struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Header    string    `json:"header"`
	Preview   string    `json:"preview"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
}

// Status is the data of the status bar.
type Status struct {
	Model         string `json:"model"`
	Entry         string `json:"entry"`
	Fallback      bool   `json:"fallback"`
	FallbackCause string `json:"fallback_cause"`
	Mode          string `json:"mode"`
	Waiting       int    `json:"waiting"`
	// Running is the sum of RunningBatches, RunningACP, and RunningShell.
	Running        int     `json:"running"`
	RunningBatches int     `json:"running_batches"`
	RunningACP     int     `json:"running_acp"`
	RunningShell   int     `json:"running_shell"`
	BrowserOn      bool    `json:"browser_on"`
	Spend24h       float64 `json:"spend24h"`
	Unpriced       bool    `json:"unpriced"`
	TLS            bool    `json:"tls"`
}

// Notice is a message that went to no live renderer.
type Notice struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Button is one button of a reply or an ack.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data"`
}

// ReplyView is the reply to a command.
type ReplyView struct {
	RequestID string     `json:"request_id"`
	Text      string     `json:"text"`
	Buttons   [][]Button `json:"buttons"`
	Edit      bool       `json:"edit"`
}

// CostView is the cost footer of a turn.
type CostView struct {
	Model      string  `json:"model"`
	Provider   string  `json:"provider"`
	Calls      int     `json:"calls"`
	In         int64   `json:"in"`
	Cached     int64   `json:"cached"`
	Out        int64   `json:"out"`
	USD        float64 `json:"usd"`
	Unpriced   bool    `json:"unpriced"`
	Seconds    float64 `json:"seconds"`
	AnsweredBy string  `json:"answered_by"`
}

// TurnDone is the data of the turn.done event.
type TurnDone struct {
	MessageID int64        `json:"message_id"`
	Error     string       `json:"error"`
	Cost      CostView     `json:"cost"`
	Context   *ContextView `json:"context"`
}

// AckView is the data of the ack event.
type AckView struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Text      string     `json:"text"`
	RequestID string     `json:"request_id"`
	Buttons   [][]Button `json:"buttons"`
}

// TaskView is the state of a background task.
type TaskView struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	State   string        `json:"state"`
	Text    string        `json:"text"`
	Subject string        `json:"subject"`          // what the task is about, for example the skill name
	Code    string        `json:"code"`             // the refusal code: need_force, need_override, or empty
	Waiting bool          `json:"waiting"`          // true while a confirm card waits
	Backup  *BackupReport `json:"backup,omitempty"` // the report of a backup task
}

// BackupReport is the result of a backup. Replaced holds key paths only, never a secret value.
type BackupReport struct {
	Size     int64    `json:"size"`
	Sessions int      `json:"sessions"`
	Messages int      `json:"messages"`
	CronJobs int      `json:"cron_jobs"`
	Replaced []string `json:"replaced"`
	Warnings []string `json:"warnings"`
	Skipped  []string `json:"skipped"`
}

// CardAnswer is the answer of a page to a card.
type CardAnswer struct {
	Answer  string   `json:"answer"`
	Choices []string `json:"choices"`
	Text    string   `json:"text"`
	Value   string   `json:"value"`
}

// FileRef is one file of a message for the page. Kind is image, audio, video, or file.
type FileRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// apiTypes holds the page-facing types in the order of registration. GenerateTypes reads it.
var apiTypes []any

// registerAPI adds page-facing types for GenerateTypes. Each api file calls it in init.
func registerAPI(vs ...any) { apiTypes = append(apiTypes, vs...) }

func init() {
	registerAPI(Snapshot{}, SessionView{}, ContextView{}, TurnView{}, ToolView{}, PlanItem{}, QueueItem{},
		Card{}, AnsweredCard{}, Status{}, Notice{}, Button{}, ReplyView{}, CostView{}, TurnDone{}, AckView{}, TaskView{}, BackupReport{},
		CardAnswer{}, FileRef{})
}
