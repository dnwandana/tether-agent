// Command soakfake serves a fake Telegram Bot API and a fake OpenAI-compatible
// provider on loopback ports for scripts/soak.sh. It never uses the real
// network.
//
// When both servers listen, soakfake writes their URLs to the -ready file as
// shell assignments (TG_URL, LLM_URL, PPROF_ADDR). PPROF_ADDR is a free
// loopback port for the pprof listener of the gateway. GET /stats on the
// Telegram server returns the counters as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	ready := flag.String("ready", "", "file that gets the server URLs when the servers listen")
	msgEvery := flag.Duration("msg-every", time.Minute, "time between two fake user messages (0 sends none)")
	userID := flag.Int64("user", 111, "Telegram user id of the fake user (also the private chat id)")
	flag.Parse()
	if *ready == "" {
		log.Fatal("soakfake: the -ready flag is missing")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot := newFakeBot(*userID)
	tgLn, tgURL := listen()
	llmLn, llmURL := listen()
	pprofLn, pprofAddr := listen()
	// The gateway listens on this port, so release it now.
	pprofLn.Close()
	pprofAddr = strings.TrimPrefix(pprofAddr, "http://")

	tgSrv := &http.Server{Handler: bot, ReadHeaderTimeout: 10 * time.Second}
	llmSrv := &http.Server{Handler: http.HandlerFunc(bot.serveLLM), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = tgSrv.Serve(tgLn) }()
	go func() { _ = llmSrv.Serve(llmLn) }()

	body := fmt.Sprintf("TG_URL=%s\nLLM_URL=%s\nPPROF_ADDR=%s\n", tgURL, llmURL, pprofAddr)
	tmp := *ready + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		log.Fatalf("soakfake: cannot write the ready file: %v", err)
	}
	if err := os.Rename(tmp, *ready); err != nil {
		log.Fatalf("soakfake: cannot write the ready file: %v", err)
	}

	if *msgEvery > 0 {
		go func() {
			tick := time.NewTicker(*msgEvery)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					bot.queueMessage()
				}
			}
		}()
	}

	<-ctx.Done()
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tgSrv.Shutdown(shut)
	_ = llmSrv.Shutdown(shut)
	b, _ := json.Marshal(bot.stats())
	fmt.Fprintf(os.Stderr, "soakfake: stopped: %s\n", b)
}

// listen opens a listener on a free loopback port and returns it with its URL.
func listen() (net.Listener, string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("soakfake: cannot listen on a loopback port: %v", err)
	}
	return ln, "http://" + ln.Addr().String()
}

// fakeBot is the fake Bot API. It keeps a queue of updates for getUpdates and
// counts the calls.
type fakeBot struct {
	userID int64

	mu      sync.Mutex
	updates []json.RawMessage
	nextUpd int64
	nextMsg int
	wake    chan struct{}
	counts  map[string]int
}

func newFakeBot(userID int64) *fakeBot {
	return &fakeBot{userID: userID, nextUpd: 1, nextMsg: 1000, wake: make(chan struct{}), counts: map[string]int{}}
}

// queueMessage adds a private text message from the fake user and wakes the
// long poll.
func (b *fakeBot) queueMessage() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextMsg++
	b.counts["user_messages"]++
	upd := map[string]any{
		"update_id": b.nextUpd,
		"message": map[string]any{
			"message_id": b.nextMsg,
			"from":       map[string]any{"id": b.userID, "is_bot": false, "first_name": "Soak", "username": "soak_user"},
			"chat":       map[string]any{"id": b.userID, "type": "private", "first_name": "Soak"},
			"date":       time.Now().Unix(),
			"text":       fmt.Sprintf("soak message %d: say hello", b.counts["user_messages"]),
		},
	}
	raw, _ := json.Marshal(upd)
	b.updates = append(b.updates, raw)
	b.nextUpd++
	close(b.wake)
	b.wake = make(chan struct{})
}

// stats returns a copy of the counters.
func (b *fakeBot) stats() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.counts))
	for k, v := range b.counts {
		out[k] = v
	}
	return out
}

func (b *fakeBot) count(name string) {
	b.mu.Lock()
	b.counts[name]++
	b.mu.Unlock()
}

// ServeHTTP serves /bot<token>/<method> and /stats.
func (b *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stats" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(b.stats())
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/bot") {
		http.NotFound(w, r)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	b.count(method)
	var params map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&params)
	}
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 99, "is_bot": true, "first_name": "Soak Bot", "username": "soak_bot"}
	case "getUpdates":
		result = b.getUpdates(r.Context(), params)
	case "sendMessage", "editMessageText", "sendPhoto", "sendDocument", "sendVoice", "sendAudio", "sendVideo":
		result = b.message(method, params)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

// getUpdates returns the queued updates from the offset. With none, it waits
// up to the timeout of the request (at most 30 s) for a new one.
func (b *fakeBot) getUpdates(ctx context.Context, params map[string]any) []json.RawMessage {
	offset := int64(number(params["offset"]))
	wait := time.Duration(number(params["timeout"])) * time.Second
	wait = min(wait, 30*time.Second)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		// The Bot API forgets the updates before the offset.
		first := b.nextUpd - int64(len(b.updates))
		for len(b.updates) > 0 && first < offset {
			b.updates = b.updates[1:]
			first++
		}
		if len(b.updates) > 0 || wait == 0 {
			out := append([]json.RawMessage{}, b.updates...)
			b.mu.Unlock()
			return out
		}
		wake := b.wake
		b.mu.Unlock()
		select {
		case <-wake:
		case <-deadline.C:
			return []json.RawMessage{}
		case <-ctx.Done():
			return []json.RawMessage{}
		}
	}
}

// message returns a Message for a send or edit method.
func (b *fakeBot) message(method string, params map[string]any) map[string]any {
	b.mu.Lock()
	id := int(number(params["message_id"]))
	if method != "editMessageText" || id == 0 {
		b.nextMsg++
		id = b.nextMsg
	}
	b.mu.Unlock()
	text, _ := params["text"].(string)
	return map[string]any{
		"message_id": id,
		"from":       map[string]any{"id": 99, "is_bot": true, "first_name": "Soak Bot", "username": "soak_bot"},
		"chat":       map[string]any{"id": number(params["chat_id"]), "type": "private"},
		"date":       time.Now().Unix(),
		"text":       text,
	}
}

// number reads a JSON number or a numeric string. Anything else is 0.
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		var f float64
		_, _ = fmt.Sscan(n, &f)
		return f
	}
	return 0
}

// serveLLM answers POST .../chat/completions with a short streamed reply in
// the OpenAI chat.completions format.
func (b *fakeBot) serveLLM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 8<<20))
	b.count("llm_calls")
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta, finish string) string {
		c := map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": nil}
		if delta != "" {
			c["delta"] = map[string]any{"role": "assistant", "content": delta}
		}
		if finish != "" {
			c["finish_reason"] = finish
		}
		raw, _ := json.Marshal(map[string]any{"id": "soak", "object": "chat.completion.chunk", "model": "soak-model",
			"choices": []any{c}})
		return "data: " + string(raw) + "\n\n"
	}
	flusher, _ := w.(http.Flusher)
	for _, part := range []string{"Hello ", "from the ", "soak provider."} {
		_, _ = io.WriteString(w, chunk(part, ""))
		if flusher != nil {
			flusher.Flush()
		}
	}
	_, _ = io.WriteString(w, chunk("", "stop"))
	usage, _ := json.Marshal(map[string]any{"id": "soak", "object": "chat.completion.chunk", "model": "soak-model",
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105}})
	_, _ = io.WriteString(w, "data: "+string(usage)+"\n\ndata: [DONE]\n\n")
}
