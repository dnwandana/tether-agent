package provider

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadSSEEventsAndMultilineData(t *testing.T) {
	in := "event: message_start\ndata: {\"a\":1}\n\n: comment\ndata: line1\ndata: line2\n\ndata: [DONE]\n\n"
	var got []string
	err := ReadSSE(strings.NewReader(in), func(ev, data string) error { got = append(got, ev+"|"+data); return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"message_start|{\"a\":1}", "|line1\nline2", "|[DONE]"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestReadSSECRLFAndNoSpaceAndTrailingEvent(t *testing.T) {
	in := "event:ping\r\ndata:x\r\n\r\nevent: e\ndata: y"
	var got []string
	err := ReadSSE(strings.NewReader(in), func(ev, data string) error { got = append(got, ev+"|"+data); return nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ping|x", "e|y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

func TestReadSSECallbackErrorStops(t *testing.T) {
	stop := errors.New("stop")
	n := 0
	err := ReadSSE(strings.NewReader("data: a\n\ndata: b\n\n"), func(string, string) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatal(err, n)
	}
}

func TestReadSSELineTooLong(t *testing.T) {
	in := "data: " + strings.Repeat("x", maxSSELine+10) + "\n\n"
	err := ReadSSE(strings.NewReader(in), func(string, string) error { return nil })
	if err == nil {
		t.Fatal("want an error for a line over the limit")
	}
}

func TestCollectAssemblesTextToolsUsage(t *testing.T) {
	ch := make(chan Event, 5)
	ch <- Event{Type: EventText, Delta: "he"}
	ch <- Event{Type: EventText, Delta: "llo"}
	ch <- Event{Type: EventToolCall, ToolCall: &ToolCall{ID: "c1", Name: "read_file", Args: `{"path":"a"}`}}
	ch <- Event{Type: EventDone, FinishReason: "tool_calls", Usage: &Usage{InputTokens: 10, OutputTokens: 3}}
	close(ch)
	var deltas []string
	r, err := Collect(ch, func(d string) { deltas = append(deltas, d) })
	if err != nil || r.Text != "hello" || len(r.ToolCalls) != 1 || r.Usage.InputTokens != 10 || len(deltas) != 2 {
		t.Fatal(r, err)
	}
	if r.FinishReason != "tool_calls" {
		t.Fatal(r.FinishReason)
	}
}

func TestCollectReturnsStreamErrorWithPartialText(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "part"}
	ch <- Event{Type: EventError, Err: io.ErrUnexpectedEOF}
	close(ch)
	r, err := Collect(ch, nil)
	if !errors.Is(err, io.ErrUnexpectedEOF) || r.Text != "part" {
		t.Fatal(r, err)
	}
}

func TestCollectMeasuresTimes(t *testing.T) {
	ch := make(chan Event)
	go func() {
		time.Sleep(20 * time.Millisecond)
		ch <- Event{Type: EventText, Delta: "a"}
		time.Sleep(20 * time.Millisecond)
		ch <- Event{Type: EventDone, FinishReason: "stop"}
		close(ch)
	}()
	r, err := Collect(ch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Usage.FirstTokenMS < 10 || r.Usage.TotalMS < r.Usage.FirstTokenMS+10 {
		t.Fatal(r.Usage)
	}
}

func TestCollectKeepsAdapterTimes(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "a"}
	ch <- Event{Type: EventDone, Usage: &Usage{FirstTokenMS: 7, TotalMS: 9}}
	close(ch)
	r, _ := Collect(ch, nil)
	if r.Usage.FirstTokenMS != 7 || r.Usage.TotalMS != 9 {
		t.Fatal(r.Usage)
	}
}

func TestCollectCopiesStamp(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "a", Provider: "fallback", Model: "m2"}
	ch <- Event{Type: EventDone, Provider: "fallback", Model: "m2"}
	close(ch)
	r, err := Collect(ch, nil)
	if err != nil || r.Provider != "fallback" || r.Model != "m2" {
		t.Fatal(r, err)
	}
}

func TestCollectStampWithNoLastEvent(t *testing.T) {
	// A /stop or a stall closes the channel with no EventDone.
	ch := make(chan Event, 1)
	ch <- Event{Type: EventText, Delta: "a", Provider: "fallback", Model: "m2"}
	close(ch)
	r, err := Collect(ch, nil)
	if err != nil || r.Text != "a" || r.Provider != "fallback" || r.Model != "m2" {
		t.Fatal(r, err)
	}
}

func TestCollectStampFromError(t *testing.T) {
	errBoom := errors.New("boom")
	ch := make(chan Event, 1)
	ch <- Event{Type: EventError, Err: errBoom, Provider: "primary", Model: "m1"}
	close(ch)
	r, err := Collect(ch, nil)
	if !errors.Is(err, errBoom) || r.Provider != "primary" || r.Model != "m1" {
		t.Fatal(r, err)
	}
}

func TestCollectNoStamp(t *testing.T) {
	ch := make(chan Event, 2)
	ch <- Event{Type: EventText, Delta: "a"}
	ch <- Event{Type: EventDone}
	close(ch)
	r, err := Collect(ch, nil)
	if err != nil || r.Provider != "" || r.Model != "" {
		t.Fatal(r, err)
	}
}
