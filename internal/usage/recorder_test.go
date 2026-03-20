package usage

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

func TestRecorderRecordNilSafe(t *testing.T) {
	var recorder *Recorder
	recorder.Record(Record{})

	recorder = &Recorder{}
	recorder.Record(Record{})
}

func TestRecorderRecordWritesStructuredFields(t *testing.T) {
	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, nil))
	recorder := NewRecorder(logger)

	recorder.Record(Record{
		RequestID:   "req-1",
		Route:       "/v1/chat/completions",
		APIKeyLabel: "test",
		Model:       "gpt-4.1",
		SessionID:   "session-1",
		Streaming:   true,
		StatusCode:  200,
		ErrorType:   "",
		Duration:    2 * time.Second,
		Usage: gatewayruntime.Usage{
			InputTokens:      11,
			OutputTokens:     5,
			CacheReadTokens:  2,
			CacheWriteTokens: 1,
		},
	})

	output := buffer.String()
	for _, needle := range []string{
		"request_id=req-1",
		"route=/v1/chat/completions",
		"api_key_label=test",
		"model=gpt-4.1",
		"session_id=session-1",
		"streaming=true",
		"status_code=200",
		"input_tokens=11",
		"output_tokens=5",
		"cache_read_tokens=2",
		"cache_write_tokens=1",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected %q in log output, got %s", needle, output)
		}
	}
}
