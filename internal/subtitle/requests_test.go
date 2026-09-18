package subtitle

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestRequestObserverCapturesResultAndModelError(t *testing.T) {
	for _, tt := range []struct{ body, status string }{{`{"text":"hello","segments":[]}`, "success"}, {`{"error":"CUDA out of memory","retryable":true}`, "error"}, {`invalid`, "error"}} {
		var events []RequestEvent
		s := &service{encoder: json.NewEncoder(io.Discard), decoder: json.NewDecoder(strings.NewReader(tt.body)), observe: func(e RequestEvent) { events = append(events, e) }}
		_, _ = s.Transcribe(context.Background(), "work/file/chunk.wav", "Japanese", 2048)
		if len(events) != 2 || events[0].Status != "pending" || events[1].Status != tt.status || events[0].ID != events[1].ID {
			t.Fatalf("events=%+v", events)
		}
		if !strings.Contains(events[0].Request, "chunk.wav") || events[1].Transport != "JSON pipe" {
			t.Fatal(events)
		}
		if tt.status == "error" && events[1].Error == "" {
			t.Fatal("missing model/transport error")
		}
	}
}
