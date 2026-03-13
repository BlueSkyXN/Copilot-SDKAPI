package usage

import (
	"log/slog"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

type Record struct {
	RequestID   string
	Route       string
	APIKeyLabel string
	Model       string
	SessionID   string
	Streaming   bool
	StatusCode  int
	ErrorType   string
	Duration    time.Duration
	Usage       gatewayruntime.Usage
}

type Recorder struct {
	logger *slog.Logger
}

func NewRecorder(logger *slog.Logger) *Recorder {
	return &Recorder{logger: logger}
}

func (r *Recorder) Record(record Record) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger.Info("gateway request completed",
		slog.String("request_id", record.RequestID),
		slog.String("route", record.Route),
		slog.String("api_key_label", record.APIKeyLabel),
		slog.String("model", record.Model),
		slog.String("session_id", record.SessionID),
		slog.Bool("streaming", record.Streaming),
		slog.Int("status_code", record.StatusCode),
		slog.String("error_type", record.ErrorType),
		slog.Duration("duration", record.Duration),
		slog.Int64("input_tokens", record.Usage.InputTokens),
		slog.Int64("output_tokens", record.Usage.OutputTokens),
		slog.Int64("cache_read_tokens", record.Usage.CacheReadTokens),
		slog.Int64("cache_write_tokens", record.Usage.CacheWriteTokens),
	)
}
