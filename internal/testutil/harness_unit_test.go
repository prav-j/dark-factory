package testutil_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/prav-j/dark-factory/internal/testutil"
)

func TestFakeClockAdvanceFiresTimers(t *testing.T) {
	start := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	clock := testutil.NewFakeClock(start)

	ch := clock.After(5 * time.Minute) // buffered; receives only when fired

	select {
	case tm := <-ch:
		t.Fatalf("timer fired before advance at %v", tm)
	default:
	}

	clock.Advance(4 * time.Minute)
	select {
	case tm := <-ch:
		t.Fatalf("timer fired early at %v", tm)
	default:
	}

	clock.Advance(1 * time.Minute)
	tm := <-ch // blocks until the timer fires
	if !tm.Equal(start.Add(5 * time.Minute)) {
		t.Fatalf("fired at %v, want %v", tm, start.Add(5*time.Minute))
	}
}

func TestFakeClockAfterWithElapsedDeadlineFiresImmediately(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC))
	clock.Advance(time.Hour)

	ch := clock.After(-time.Minute) // deadline already in the past
	select {
	case <-ch:
	default:
		t.Fatal("elapsed-deadline timer did not fire immediately")
	}
}

func TestFakeLLMScriptedResponses(t *testing.T) {
	script := []testutil.LLMResponse{
		{Content: "step one", StopReason: "tool_use", ToolName: "search"},
		{Content: "done", StopReason: "end_turn"},
	}
	llm := testutil.NewFakeLLM(t, script...)

	post := func(content string) testutil.LLMResponse {
		body := `{"model":"test-model","messages":[{"role":"user","content":"` + content + `"}]}`
		resp, err := http.Post(llm.URL()+"/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		var out testutil.LLMResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	first := post("first")
	if first.StopReason != "tool_use" || first.ToolName != "search" {
		t.Fatalf("first response = %+v, want scripted tool_use/search", first)
	}
	last := post("second")
	if last.Content != "done" {
		t.Fatalf("second response = %+v", last)
	}
	repeat := post("third") // script exhausted -> last response repeats
	if repeat.Content != "done" {
		t.Fatalf("exhausted script should repeat last response, got %+v", repeat)
	}

	reqs := llm.Requests()
	if len(reqs) != 3 {
		t.Fatalf("captured %d requests, want 3", len(reqs))
	}
	if reqs[0].Messages[0].Content != "first" {
		t.Fatalf("first message = %q", reqs[0].Messages[0].Content)
	}
}
