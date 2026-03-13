package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"copilot-sdkapi/internal/auth"
	"copilot-sdkapi/internal/compat"
	"copilot-sdkapi/internal/config"
	gatewayruntime "copilot-sdkapi/internal/runtime"
	"copilot-sdkapi/internal/session"
	"copilot-sdkapi/internal/usage"
)

var errStreamIdleTimeout = errors.New("stream idle timeout exceeded")
var errInvalidSessionKey = errors.New("invalid session key")
var errInvalidAnthropicVersion = errors.New("unsupported anthropic-version header")
var errUnsupportedReasoningEffort = errors.New("reasoning effort is not supported by the selected model")
var errUnsupportedVisionModel = errors.New("image input is not supported by the selected model")
var errModelLimitsExceeded = errors.New("request exceeds model capability limits")
var errInvalidSystemMessageMode = errors.New("unsupported x_copilot.system_message_mode")
var errUnknownAgent = errors.New("unknown x_copilot.agent")

const supportedAnthropicVersion = "2023-06-01"

type Server struct {
	cfg      config.Config
	auth     *auth.Store
	provider gatewayruntime.Provider
	sessions *session.Manager
	recorder *usage.Recorder
	logger   *slog.Logger
	mux      *http.ServeMux
}

type preparedConversation struct {
	lease                *session.Lease
	model                string
	message              gatewayruntime.MessageOptions
	cleanup              func() error
	agent                string
	systemMessageMode    string
	includeReasoning     bool
	includeRuntimeEvents bool
	copilotRequested     bool
}

func NewServer(
	cfg config.Config,
	authStore *auth.Store,
	provider gatewayruntime.Provider,
	sessionManager *session.Manager,
	recorder *usage.Recorder,
	logger *slog.Logger,
) *Server {
	server := &Server{
		cfg:      cfg,
		auth:     authStore,
		provider: provider,
		sessions: sessionManager,
		recorder: recorder,
		logger:   logger,
		mux:      http.NewServeMux(),
	}
	server.routes()
	return server
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/chat/completions", s.handleOpenAIChatCompletions)
	s.mux.HandleFunc("/v1/messages", s.handleClaudeMessages)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, err := s.auth.AuthenticateRequest(r); err != nil {
		s.writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	models, err := s.provider.ListModels(ctx)
	if err != nil {
		status, errorType, message := classifyError(err)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}
	writeJSON(w, http.StatusOK, compat.BuildOpenAIModelsResponse(models))
}

func (s *Server) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestID := newRequestID()
	started := time.Now()
	statusCode := http.StatusOK
	errorType := ""
	model := ""
	sessionID := ""
	responseUsage := gatewayruntime.Usage{}

	identity, err := s.auth.AuthenticateRequest(r)
	if err != nil {
		s.writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	conversation, err := compat.ParseOpenAIChatCompletionRequest(body)
	if err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeOpenAIError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}

	model = s.resolveModel(conversation.Model)
	publicSessionKey := sessionKeyFromRequest(r)
	if err := validateSessionKey(publicSessionKey); err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeOpenAIError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, conversation.Stream, statusCode, errorType, started, responseUsage)
		return
	}
	scopedKey := scopedSessionKey(identity, publicSessionKey)
	responseID := "chatcmpl-" + requestID

	if conversation.Stream {
		requestCtx, cancelRequest := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancelRequest()
		streamCtx, cancelStream, touch := newIdleContext(requestCtx, s.cfg.StreamIdleTimeout)
		defer cancelStream(nil)

		prepared, err := s.prepareConversation(streamCtx, scopedKey, conversation)
		if err != nil {
			statusCode, errorType, message := classifyError(err)
			s.writeOpenAIError(w, statusCode, errorType, message)
			s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, true, statusCode, errorType, started, responseUsage)
			return
		}
		discarded := false
		defer func() {
			if prepared.cleanup != nil {
				_ = prepared.cleanup()
			}
			if discarded {
				_ = prepared.lease.Discard()
				return
			}
			_ = prepared.lease.Release()
		}()

		sessionID = prepared.lease.SessionID()
		model = prepared.model
		statusCode, errorType, responseUsage = s.streamOpenAIChatCompletion(streamCtx, w, prepared, responseID, publicSessionKey, touch)
		if errorType != "" {
			discarded = true
		}
		s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, true, statusCode, errorType, started, responseUsage)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	prepared, err := s.prepareConversation(ctx, scopedKey, conversation)
	if err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeOpenAIError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}
	discarded := false
	defer func() {
		if prepared.cleanup != nil {
			_ = prepared.cleanup()
		}
		if discarded {
			_ = prepared.lease.Discard()
			return
		}
		_ = prepared.lease.Release()
	}()

	sessionID = prepared.lease.SessionID()
	model = prepared.model
	result, err := prepared.lease.Session().Send(ctx, prepared.message, nil)
	if err != nil {
		discarded = true
		statusCode, errorType, message := classifyError(err)
		s.writeOpenAIError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/chat/completions", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}
	responseUsage = result.Usage
	if publicSessionKey != "" {
		w.Header().Set("X-Session-ID", publicSessionKey)
	}
	writeJSON(w, http.StatusOK, compat.BuildOpenAIChatCompletionResponse(responseID, model, result.Content, result.Usage, s.buildCopilotResponseExtension(prepared, result)))
	s.recordUsage(identity, requestID, "/v1/chat/completions", model, result.SessionID, false, http.StatusOK, "", started, responseUsage)
}

func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	requestID := newRequestID()
	started := time.Now()
	statusCode := http.StatusOK
	errorType := ""
	model := ""
	sessionID := ""
	responseUsage := gatewayruntime.Usage{}

	identity, err := s.auth.AuthenticateRequest(r)
	if err != nil {
		s.writeClaudeError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
		return
	}
	if err := validateAnthropicVersion(r.Header.Get("anthropic-version")); err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeClaudeError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	conversation, err := compat.ParseClaudeMessagesRequest(body)
	if err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeClaudeError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}

	model = s.resolveModel(conversation.Model)
	publicSessionKey := sessionKeyFromRequest(r)
	if err := validateSessionKey(publicSessionKey); err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeClaudeError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, conversation.Stream, statusCode, errorType, started, responseUsage)
		return
	}
	scopedKey := scopedSessionKey(identity, publicSessionKey)
	responseID := "msg_" + requestID

	if conversation.Stream {
		requestCtx, cancelRequest := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancelRequest()
		streamCtx, cancelStream, touch := newIdleContext(requestCtx, s.cfg.StreamIdleTimeout)
		defer cancelStream(nil)

		prepared, err := s.prepareConversation(streamCtx, scopedKey, conversation)
		if err != nil {
			statusCode, errorType, message := classifyError(err)
			s.writeClaudeError(w, statusCode, errorType, message)
			s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, true, statusCode, errorType, started, responseUsage)
			return
		}
		discarded := false
		defer func() {
			if prepared.cleanup != nil {
				_ = prepared.cleanup()
			}
			if discarded {
				_ = prepared.lease.Discard()
				return
			}
			_ = prepared.lease.Release()
		}()

		sessionID = prepared.lease.SessionID()
		model = prepared.model
		statusCode, errorType, responseUsage = s.streamClaudeMessage(streamCtx, w, prepared, responseID, publicSessionKey, touch)
		if errorType != "" {
			discarded = true
		}
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, true, statusCode, errorType, started, responseUsage)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	prepared, err := s.prepareConversation(ctx, scopedKey, conversation)
	if err != nil {
		statusCode, errorType, message := classifyError(err)
		s.writeClaudeError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}
	discarded := false
	defer func() {
		if prepared.cleanup != nil {
			_ = prepared.cleanup()
		}
		if discarded {
			_ = prepared.lease.Discard()
			return
		}
		_ = prepared.lease.Release()
	}()

	sessionID = prepared.lease.SessionID()
	model = prepared.model
	result, err := prepared.lease.Session().Send(ctx, prepared.message, nil)
	if err != nil {
		discarded = true
		statusCode, errorType, message := classifyError(err)
		s.writeClaudeError(w, statusCode, errorType, message)
		s.recordUsage(identity, requestID, "/v1/messages", model, sessionID, false, statusCode, errorType, started, responseUsage)
		return
	}
	responseUsage = result.Usage
	if publicSessionKey != "" {
		w.Header().Set("X-Session-ID", publicSessionKey)
	}
	writeJSON(w, http.StatusOK, compat.BuildClaudeMessageResponse(responseID, model, result.Content, result.Usage, s.buildCopilotResponseExtension(prepared, result)))
	s.recordUsage(identity, requestID, "/v1/messages", model, result.SessionID, false, http.StatusOK, "", started, responseUsage)
}

func (s *Server) prepareConversation(ctx context.Context, externalSessionKey string, request compat.ConversationRequest) (*preparedConversation, error) {
	model := s.resolveModel(request.Model)
	if err := s.validateConversationFeatures(ctx, model, request); err != nil {
		return nil, err
	}
	systemMessageMode := effectiveSystemMessageMode(request.Copilot.SystemMessageMode)
	agent := effectiveAgent(request.Copilot.Agent, s.cfg.SDKDefaultAgent)
	spec := session.Spec{
		Model:            model,
		SystemPrompt:     request.SystemPrompt,
		SystemPromptMode: systemMessageMode,
		ReasoningEffort:  request.ReasoningEffort,
		Agent:            agent,
	}
	lease, err := s.sessions.Acquire(ctx, externalSessionKey, spec, func(factoryCtx context.Context) (gatewayruntime.Session, error) {
		return s.provider.NewSession(factoryCtx, gatewayruntime.SessionOptions{
			Model:            model,
			SystemPrompt:     request.SystemPrompt,
			SystemPromptMode: systemMessageMode,
			ReasoningEffort:  request.ReasoningEffort,
			Agent:            agent,
		})
	})
	if err != nil {
		return nil, err
	}

	prompt := ""
	if lease.Persistent() && !lease.Created() {
		prompt, err = compat.LatestUserPrompt(request.Turns)
		if err != nil {
			lease.Release()
			return nil, err
		}
	} else {
		prompt = compat.BuildPrompt(request.Turns)
	}

	latestTurn, err := compat.LatestUserTurn(request.Turns)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	attachments, cleanup, err := s.materializeAttachments(latestTurn.Attachments)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}

	return &preparedConversation{
		lease: lease,
		model: model,
		message: gatewayruntime.MessageOptions{
			Prompt:      prompt,
			Attachments: attachments,
		},
		cleanup:              cleanup,
		agent:                agent,
		systemMessageMode:    systemMessageMode,
		includeReasoning:     request.Copilot.IncludeReasoning,
		includeRuntimeEvents: request.Copilot.IncludeRuntimeEvents,
		copilotRequested:     hasCopilotRequestExtension(request.Copilot),
	}, nil
}

func (s *Server) streamOpenAIChatCompletion(ctx context.Context, w http.ResponseWriter, prepared *preparedConversation, responseID string, publicSessionKey string, touch func()) (int, string, gatewayruntime.Usage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return http.StatusInternalServerError, "internal_error", gatewayruntime.Usage{}
	}
	writeTimeout := s.streamWriteTimeout()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if publicSessionKey != "" {
		w.Header().Set("X-Session-ID", publicSessionKey)
	}
	if err := setWriteDeadline(w, writeTimeout); err != nil {
		return http.StatusInternalServerError, "stream_write_error", gatewayruntime.Usage{}
	}
	w.WriteHeader(http.StatusOK)

	if err := writeOpenAISSE(w, flusher, writeTimeout, compat.BuildOpenAIStreamChunk(responseID, prepared.model, compat.OpenAIDelta{Role: "assistant"}, nil, nil)); err != nil {
		return http.StatusOK, "stream_write_error", gatewayruntime.Usage{}
	}

	var latestUsage gatewayruntime.Usage
	touch()
	result, err := prepared.lease.Session().Send(ctx, prepared.message, func(event gatewayruntime.Event) error {
		touch()
		switch event.Type {
		case gatewayruntime.EventMessageDelta:
			return writeOpenAISSE(w, flusher, writeTimeout, compat.BuildOpenAIStreamChunk(responseID, prepared.model, compat.OpenAIDelta{Content: event.Delta}, nil, nil))
		case gatewayruntime.EventUsage:
			latestUsage = event.Usage
		default:
			if extension := s.openAIStreamExtension(event, prepared); extension != nil {
				return writeOpenAISSE(w, flusher, writeTimeout, compat.BuildOpenAIStreamChunk(responseID, prepared.model, compat.OpenAIDelta{}, nil, extension))
			}
		}
		return nil
	})
	if err != nil {
		status, errorType, message := classifyError(err)
		_ = writeOpenAISSE(w, flusher, writeTimeout, map[string]any{
			"error": map[string]any{
				"message": message,
				"type":    errorType,
			},
		})
		_ = writeDone(w, flusher, writeTimeout)
		return status, errorType, latestUsage
	}
	finishReason := "stop"
	if err := writeOpenAISSE(w, flusher, writeTimeout, compat.BuildOpenAIStreamChunk(responseID, prepared.model, compat.OpenAIDelta{}, &finishReason, nil)); err != nil {
		return http.StatusOK, "stream_write_error", result.Usage
	}
	_ = writeDone(w, flusher, writeTimeout)
	return http.StatusOK, "", result.Usage
}

func (s *Server) streamClaudeMessage(ctx context.Context, w http.ResponseWriter, prepared *preparedConversation, responseID string, publicSessionKey string, touch func()) (int, string, gatewayruntime.Usage) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return http.StatusInternalServerError, "internal_error", gatewayruntime.Usage{}
	}
	writeTimeout := s.streamWriteTimeout()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if publicSessionKey != "" {
		w.Header().Set("X-Session-ID", publicSessionKey)
	}
	if err := setWriteDeadline(w, writeTimeout); err != nil {
		return http.StatusInternalServerError, "stream_write_error", gatewayruntime.Usage{}
	}
	w.WriteHeader(http.StatusOK)

	if err := writeClaudeSSE(w, flusher, writeTimeout, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            responseID,
			"type":          "message",
			"role":          "assistant",
			"model":         prepared.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{},
		},
	}); err != nil {
		return http.StatusOK, "stream_write_error", gatewayruntime.Usage{}
	}
	if err := writeClaudeSSE(w, flusher, writeTimeout, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	}); err != nil {
		return http.StatusOK, "stream_write_error", gatewayruntime.Usage{}
	}

	var latestUsage gatewayruntime.Usage
	touch()
	result, err := prepared.lease.Session().Send(ctx, prepared.message, func(event gatewayruntime.Event) error {
		touch()
		switch event.Type {
		case gatewayruntime.EventMessageDelta:
			return writeClaudeSSE(w, flusher, writeTimeout, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{
					"type": "text_delta",
					"text": event.Delta,
				},
			})
		case gatewayruntime.EventUsage:
			latestUsage = event.Usage
		default:
			if payload := s.claudeStreamRuntimePayload(event, prepared); payload != nil {
				return writeClaudeSSE(w, flusher, writeTimeout, "x_copilot", payload)
			}
		}
		return nil
	})
	if err != nil {
		status, errorType, message := classifyError(err)
		_ = writeClaudeSSE(w, flusher, writeTimeout, "error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errorType,
				"message": message,
			},
		})
		_ = writeClaudeSSE(w, flusher, writeTimeout, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": 0,
		})
		_ = writeClaudeSSE(w, flusher, writeTimeout, "message_stop", map[string]any{
			"type": "message_stop",
		})
		return status, errorType, latestUsage
	}

	if err := writeClaudeSSE(w, flusher, writeTimeout, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	}); err != nil {
		return http.StatusOK, "stream_write_error", result.Usage
	}
	if err := writeClaudeSSE(w, flusher, writeTimeout, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"input_tokens":                result.Usage.InputTokens,
			"output_tokens":               result.Usage.OutputTokens,
			"cache_creation_input_tokens": result.Usage.CacheWriteTokens,
			"cache_read_input_tokens":     result.Usage.CacheReadTokens,
		},
	}); err != nil {
		return http.StatusOK, "stream_write_error", result.Usage
	}
	if err := writeClaudeSSE(w, flusher, writeTimeout, "message_stop", map[string]any{
		"type": "message_stop",
	}); err != nil {
		return http.StatusOK, "stream_write_error", result.Usage
	}
	return http.StatusOK, "", result.Usage
}

func (s *Server) buildCopilotResponseExtension(prepared *preparedConversation, result gatewayruntime.Result) *compat.CopilotResponseExtension {
	if prepared == nil {
		return nil
	}

	extension := &compat.CopilotResponseExtension{}
	if prepared.copilotRequested {
		if prepared.agent != "" {
			extension.Agent = prepared.agent
		}
		if prepared.systemMessageMode == "replace" {
			extension.SystemMessageMode = prepared.systemMessageMode
		}
	}
	if prepared.includeReasoning && strings.TrimSpace(result.Reasoning) != "" {
		extension.Reasoning = result.Reasoning
	}
	if prepared.includeRuntimeEvents && len(result.RuntimeEvents) > 0 {
		extension.RuntimeEvents = append([]gatewayruntime.RuntimeEvent(nil), result.RuntimeEvents...)
	}
	if extension.Agent == "" && extension.SystemMessageMode == "" && extension.Reasoning == "" && len(extension.RuntimeEvents) == 0 {
		return nil
	}
	return extension
}

func (s *Server) openAIStreamExtension(event gatewayruntime.Event, prepared *preparedConversation) *compat.OpenAIStreamExtension {
	runtimeEvent := streamRuntimeEvent(event, prepared)
	if runtimeEvent == nil {
		return nil
	}
	return &compat.OpenAIStreamExtension{Event: runtimeEvent}
}

func (s *Server) claudeStreamRuntimePayload(event gatewayruntime.Event, prepared *preparedConversation) map[string]any {
	runtimeEvent := streamRuntimeEvent(event, prepared)
	if runtimeEvent == nil {
		return nil
	}
	return map[string]any{
		"type":  "x_copilot",
		"event": runtimeEvent,
	}
}

func streamRuntimeEvent(event gatewayruntime.Event, prepared *preparedConversation) *gatewayruntime.RuntimeEvent {
	if prepared == nil || event.Runtime == nil {
		return nil
	}
	if isReasoningEvent(event.Type) {
		if !prepared.includeReasoning && !prepared.includeRuntimeEvents {
			return nil
		}
	} else if !prepared.includeRuntimeEvents {
		return nil
	}
	return cloneRuntimeEvent(event.Runtime)
}

func isReasoningEvent(eventType gatewayruntime.EventType) bool {
	return eventType == gatewayruntime.EventReasoning || eventType == gatewayruntime.EventReasoningDelta
}

func cloneRuntimeEvent(input *gatewayruntime.RuntimeEvent) *gatewayruntime.RuntimeEvent {
	if input == nil {
		return nil
	}
	cloned := *input
	if input.Success != nil {
		value := *input.Success
		cloned.Success = &value
	}
	if input.Telemetry != nil {
		cloned.Telemetry = cloneAny(input.Telemetry).(map[string]any)
	}
	if input.PermissionRequest != nil {
		permission := *input.PermissionRequest
		permission.Commands = append([]string(nil), input.PermissionRequest.Commands...)
		if input.PermissionRequest.ReadOnly != nil {
			value := *input.PermissionRequest.ReadOnly
			permission.ReadOnly = &value
		}
		cloned.PermissionRequest = &permission
	}
	if input.UserInputRequest != nil {
		userInput := *input.UserInputRequest
		userInput.Choices = append([]string(nil), input.UserInputRequest.Choices...)
		if input.UserInputRequest.AllowFreeform != nil {
			value := *input.UserInputRequest.AllowFreeform
			userInput.AllowFreeform = &value
		}
		if input.UserInputRequest.RequestedSchema != nil {
			userInput.RequestedSchema = cloneAny(input.UserInputRequest.RequestedSchema).(map[string]any)
		}
		cloned.UserInputRequest = &userInput
	}
	cloned.Arguments = cloneAny(input.Arguments)
	cloned.AllowedTools = append([]string(nil), input.AllowedTools...)
	return &cloned
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, item := range typed {
			cloned[key] = cloneAny(item)
		}
		return cloned
	case []any:
		cloned := make([]any, len(typed))
		for idx, item := range typed {
			cloned[idx] = cloneAny(item)
		}
		return cloned
	default:
		return typed
	}
}

func (s *Server) resolveModel(model string) string {
	model = strings.TrimSpace(model)
	if model != "" {
		return model
	}
	return s.cfg.DefaultModel
}

func (s *Server) recordUsage(identity auth.Identity, requestID, route, model, sessionID string, streaming bool, statusCode int, errorType string, started time.Time, runtimeUsage gatewayruntime.Usage) {
	s.recorder.Record(usage.Record{
		RequestID:   requestID,
		Route:       route,
		APIKeyLabel: identity.Label,
		Model:       model,
		SessionID:   sessionID,
		Streaming:   streaming,
		StatusCode:  statusCode,
		ErrorType:   errorType,
		Duration:    time.Since(started),
		Usage:       runtimeUsage,
	})
}

func (s *Server) writeOpenAIError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
		},
	})
}

func (s *Server) writeClaudeError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errorType,
			"message": message,
		},
	})
}

func classifyError(err error) (int, string, string) {
	var runtimeErr *gatewayruntime.ResponseError
	if errors.As(err, &runtimeErr) {
		status := runtimeErr.StatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		errorType := runtimeErr.Type
		if errorType == "" {
			errorType = "upstream_error"
		}
		return status, errorType, runtimeErr.Message
	}
	switch {
	case errors.Is(err, errStreamIdleTimeout), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout_error", err.Error()
	case errors.Is(err, context.Canceled):
		return http.StatusServiceUnavailable, "cancelled", "request cancelled"
	case errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized, "authentication_error", "invalid API key"
	case errors.Is(err, compat.ErrUnsupportedTools), errors.Is(err, compat.ErrUnsupportedControls), errors.Is(err, compat.ErrUnsupportedMessageFields):
		return http.StatusBadRequest, "unsupported_feature", err.Error()
	case errors.Is(err, compat.ErrUnsupportedContent), errors.Is(err, compat.ErrNoTurns), errors.Is(err, compat.ErrFinalTurnNotUser), errors.Is(err, compat.ErrUnsupportedImages), errors.Is(err, compat.ErrInvalidImageData):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case isMaxBytesError(err):
		return http.StatusRequestEntityTooLarge, "invalid_request_error", err.Error()
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return http.StatusBadRequest, "invalid_request_error", "request body must be valid JSON"
	case isJSONDecodeError(err):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInvalidSessionKey):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInvalidAnthropicVersion):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInvalidSystemMessageMode), errors.Is(err, errUnknownAgent):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errUnsupportedReasoningEffort), errors.Is(err, errUnsupportedVisionModel), errors.Is(err, errModelLimitsExceeded):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, session.ErrSessionSpecMismatch):
		return http.StatusConflict, "session_conflict", err.Error()
	case errors.Is(err, session.ErrTooManySessionsForNamespace):
		return http.StatusTooManyRequests, "session_limit_exceeded", err.Error()
	case errors.Is(err, session.ErrTooManySessions):
		return http.StatusServiceUnavailable, "session_capacity_exceeded", err.Error()
	default:
		return http.StatusBadGateway, "upstream_error", err.Error()
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeOpenAISSE(w http.ResponseWriter, flusher http.Flusher, writeTimeout time.Duration, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := setWriteDeadline(w, writeTimeout); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeClaudeSSE(w http.ResponseWriter, flusher http.Flusher, writeTimeout time.Duration, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := setWriteDeadline(w, writeTimeout); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeDone(w http.ResponseWriter, flusher http.Flusher, writeTimeout time.Duration) error {
	if err := setWriteDeadline(w, writeTimeout); err != nil {
		return err
	}
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) streamWriteTimeout() time.Duration {
	if s.cfg.StreamIdleTimeout > 0 {
		return s.cfg.StreamIdleTimeout
	}
	return s.cfg.RequestTimeout
}

func setWriteDeadline(w http.ResponseWriter, writeTimeout time.Duration) error {
	if writeTimeout <= 0 {
		return nil
	}
	err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeTimeout))
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func sessionKeyFromRequest(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("X-Session-ID"))
	if value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get("X-Copilot-Session-ID"))
}

func validateSessionKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) > 128 {
		return fmt.Errorf("%w: max length is 128", errInvalidSessionKey)
	}
	for _, ch := range key {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.', ch == ':':
		default:
			return fmt.Errorf("%w: only letters, digits, dot, underscore, colon, and dash are allowed", errInvalidSessionKey)
		}
	}
	return nil
}

func validateAnthropicVersion(value string) error {
	if strings.TrimSpace(value) != supportedAnthropicVersion {
		return fmt.Errorf("%w: expected %s", errInvalidAnthropicVersion, supportedAnthropicVersion)
	}
	return nil
}

func (s *Server) validateConversationFeatures(ctx context.Context, modelID string, request compat.ConversationRequest) error {
	systemMessageMode := effectiveSystemMessageMode(request.Copilot.SystemMessageMode)
	switch systemMessageMode {
	case "append", "replace":
	default:
		return fmt.Errorf("%w: %s", errInvalidSystemMessageMode, request.Copilot.SystemMessageMode)
	}
	if systemMessageMode == "replace" && strings.TrimSpace(request.SystemPrompt) == "" {
		return fmt.Errorf("%w: replace mode requires a non-empty system prompt", errInvalidSystemMessageMode)
	}
	if agent := strings.TrimSpace(request.Copilot.Agent); agent != "" && !s.hasCustomAgent(agent) {
		return fmt.Errorf("%w: %s", errUnknownAgent, agent)
	}

	if err := compat.ValidateAttachmentPlacement(request.Turns, false); err != nil {
		return err
	}

	if request.ReasoningEffort == "" && attachmentCount(request.Turns) == 0 {
		return nil
	}

	model, err := s.lookupModel(ctx, modelID)
	if err != nil || model == nil {
		return err
	}

	if request.ReasoningEffort != "" {
		if !model.Supports.ReasoningEffort {
			return errUnsupportedReasoningEffort
		}
		if len(model.SupportedReasoningEfforts) > 0 {
			supported := false
			for _, effort := range model.SupportedReasoningEfforts {
				if request.ReasoningEffort == effort {
					supported = true
					break
				}
			}
			if !supported {
				return fmt.Errorf("%w: %s", errUnsupportedReasoningEffort, request.ReasoningEffort)
			}
		}
	}

	latest, err := compat.LatestUserTurn(request.Turns)
	if err != nil {
		return err
	}
	if len(latest.Attachments) == 0 {
		return nil
	}
	if !model.Supports.Vision {
		return errUnsupportedVisionModel
	}
	if model.Limits.Vision == nil {
		return nil
	}
	if limit := model.Limits.Vision.MaxPromptImages; limit > 0 && len(latest.Attachments) > limit {
		return fmt.Errorf("%w: max_prompt_images=%d", errModelLimitsExceeded, limit)
	}
	if len(model.Limits.Vision.SupportedMediaTypes) > 0 {
		allowed := make(map[string]struct{}, len(model.Limits.Vision.SupportedMediaTypes))
		for _, mediaType := range model.Limits.Vision.SupportedMediaTypes {
			allowed[strings.ToLower(strings.TrimSpace(mediaType))] = struct{}{}
		}
		for _, attachment := range latest.Attachments {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(attachment.MediaType))]; !ok {
				return fmt.Errorf("%w: unsupported media type %s", errModelLimitsExceeded, attachment.MediaType)
			}
		}
	}
	if limit := model.Limits.Vision.MaxPromptImageSize; limit > 0 {
		for _, attachment := range latest.Attachments {
			if len(attachment.Data) > limit {
				return fmt.Errorf("%w: image exceeds max_prompt_image_size", errModelLimitsExceeded)
			}
		}
	}
	return nil
}

func attachmentCount(turns []compat.Turn) int {
	count := 0
	for _, turn := range turns {
		count += len(turn.Attachments)
	}
	return count
}

func (s *Server) lookupModel(ctx context.Context, modelID string) (*gatewayruntime.Model, error) {
	models, err := s.provider.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for _, model := range models {
		if model.ID == modelID {
			copy := model
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Server) hasCustomAgent(agent string) bool {
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return false
	}
	for _, configured := range s.cfg.SDKCustomAgents {
		if strings.TrimSpace(configured.Name) == agent {
			return true
		}
	}
	return false
}

func effectiveSystemMessageMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "replace":
		return "replace"
	case "", "append":
		return "append"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func effectiveAgent(requestAgent, defaultAgent string) string {
	if strings.TrimSpace(requestAgent) != "" {
		return strings.TrimSpace(requestAgent)
	}
	return strings.TrimSpace(defaultAgent)
}

func hasCopilotRequestExtension(extension compat.CopilotRequestExtension) bool {
	return extension.IncludeReasoning ||
		extension.IncludeRuntimeEvents ||
		strings.TrimSpace(extension.Agent) != "" ||
		strings.TrimSpace(extension.SystemMessageMode) != ""
}

func (s *Server) materializeAttachments(images []compat.ImageAttachment) ([]gatewayruntime.Attachment, func() error, error) {
	if len(images) == 0 {
		return nil, nil, nil
	}

	dir, err := os.MkdirTemp("", "copilot-sdkapi-images-*")
	if err != nil {
		return nil, nil, err
	}

	attachments := make([]gatewayruntime.Attachment, 0, len(images))
	cleanup := func() error {
		if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	for _, image := range images {
		file, err := os.CreateTemp(dir, "image-*"+extensionForMediaType(image.MediaType))
		if err != nil {
			_ = cleanup()
			return nil, nil, err
		}
		if _, err := file.Write(image.Data); err != nil {
			name := file.Name()
			_ = file.Close()
			_ = os.Remove(name)
			_ = cleanup()
			return nil, nil, err
		}
		if err := file.Close(); err != nil {
			name := file.Name()
			_ = os.Remove(name)
			_ = cleanup()
			return nil, nil, err
		}
		attachments = append(attachments, gatewayruntime.Attachment{
			Path:      file.Name(),
			MediaType: image.MediaType,
		})
	}

	return attachments, cleanup, nil
}

func extensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

func scopedSessionKey(identity auth.Identity, publicSessionKey string) string {
	if publicSessionKey == "" {
		return ""
	}
	return identity.SessionNamespace + ":" + publicSessionKey
}

func newRequestID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func newIdleContext(parent context.Context, idle time.Duration) (context.Context, context.CancelCauseFunc, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	if idle <= 0 {
		return ctx, cancel, func() {}
	}

	timer := time.NewTimer(idle)
	go func() {
		select {
		case <-timer.C:
			cancel(errStreamIdleTimeout)
		case <-ctx.Done():
		}
	}()

	touch := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idle)
	}

	return ctx, func(err error) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		cancel(err)
	}, touch
}

func isJSONDecodeError(err error) bool {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &typeErr)
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}
