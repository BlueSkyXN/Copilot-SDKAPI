package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
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
var errMissingSessionKey = errors.New("X-Session-ID header is required")
var errInvalidAnthropicVersion = errors.New("unsupported anthropic-version header")
var errUnsupportedReasoningEffort = errors.New("reasoning effort is not supported by the selected model")
var errUnsupportedVisionModel = errors.New("image input is not supported by the selected model")
var errModelLimitsExceeded = errors.New("request exceeds model capability limits")
var errUnknownModelCapabilities = errors.New("requested model is not listed in /v1/models")
var errInvalidSystemMessageMode = errors.New("unsupported x_copilot.system_message_mode")
var errUnknownAgent = errors.New("unknown x_copilot.agent")
var errInteractiveRequiresStream = errors.New("x_copilot continuation flows require stream=true")
var errInteractiveRequiresSessionKey = errors.New("x_copilot continuation flows require X-Session-ID")
var errInvalidCopilotPermissionMode = errors.New("unsupported x_copilot.permission_mode")
var errCopilotPermissionEscalation = errors.New("x_copilot.permission_mode cannot exceed server permission policy")
var errInvalidCopilotTool = errors.New("invalid x_copilot.tools entry")
var errInvalidCopilotResponse = errors.New("invalid x_copilot continuation request")
var errInvalidCopilotAttachment = errors.New("invalid x_copilot attachment")

const supportedAnthropicVersion = "2023-06-01"

var blockedRemoteIPPrefixes = []netip.Prefix{
	mustParsePrefix("0.0.0.0/8"),
	mustParsePrefix("10.0.0.0/8"),
	mustParsePrefix("100.64.0.0/10"),
	mustParsePrefix("127.0.0.0/8"),
	mustParsePrefix("169.254.0.0/16"),
	mustParsePrefix("172.16.0.0/12"),
	mustParsePrefix("192.0.0.0/24"),
	mustParsePrefix("192.0.2.0/24"),
	mustParsePrefix("192.88.99.0/24"),
	mustParsePrefix("192.168.0.0/16"),
	mustParsePrefix("198.18.0.0/15"),
	mustParsePrefix("198.51.100.0/24"),
	mustParsePrefix("203.0.113.0/24"),
	mustParsePrefix("224.0.0.0/4"),
	mustParsePrefix("240.0.0.0/4"),
	mustParsePrefix("::/128"),
	mustParsePrefix("::1/128"),
	mustParsePrefix("100::/64"),
	mustParsePrefix("2001:2::/48"),
	mustParsePrefix("2001:10::/28"),
	mustParsePrefix("2001:db8::/32"),
	mustParsePrefix("fc00::/7"),
	mustParsePrefix("fe80::/10"),
	mustParsePrefix("ff00::/8"),
}

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
	s.mux.HandleFunc("/v1/copilot/respond", s.handleCopilotResponse)
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
		clearActivity := s.sessions.SetActivityHook(scopedKey, touch)
		defer clearActivity()

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
		clearActivity := s.sessions.SetActivityHook(scopedKey, touch)
		defer clearActivity()

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
	permissionMode, err := effectivePermissionMode(request.Copilot.PermissionMode, s.cfg.SDKPermissionMode)
	if err != nil {
		return nil, err
	}
	requiresContinuation := request.Copilot.Interactive || len(request.Copilot.Tools) > 0 || permissionMode == gatewayruntime.PermissionModeBridge
	if externalSessionKey == "" && requiresContinuation {
		return nil, errInteractiveRequiresSessionKey
	}
	if err := s.validateConversationFeatures(ctx, model, request); err != nil {
		return nil, err
	}
	systemMessageMode := effectiveSystemMessageMode(request.Copilot.SystemMessageMode)
	agent := effectiveAgent(request.Copilot.Agent, s.cfg.SDKDefaultAgent)
	runtimeTools := toRuntimeTools(request.Copilot.Tools)
	toolsFingerprint, err := toolDefinitionsFingerprint(request.Copilot.Tools)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidCopilotTool, err)
	}
	spec := session.Spec{
		Model:            model,
		SystemPrompt:     request.SystemPrompt,
		SystemPromptMode: systemMessageMode,
		ReasoningEffort:  request.ReasoningEffort,
		Agent:            agent,
		Interactive:      request.Copilot.Interactive,
		ToolsFingerprint: toolsFingerprint,
		PermissionMode:   permissionMode,
	}
	lease, err := s.sessions.Acquire(ctx, externalSessionKey, spec, func(factoryCtx context.Context) (gatewayruntime.Session, error) {
		return s.provider.NewSession(factoryCtx, gatewayruntime.SessionOptions{
			SessionID:        externalSessionKey,
			Model:            model,
			SystemPrompt:     request.SystemPrompt,
			SystemPromptMode: systemMessageMode,
			ReasoningEffort:  request.ReasoningEffort,
			Agent:            agent,
			Interactive:      request.Copilot.Interactive,
			Tools:            runtimeTools,
			PermissionMode:   permissionMode,
		})
	}, func(factoryCtx context.Context, sessionID string) (gatewayruntime.Session, error) {
		return s.provider.ResumeSession(factoryCtx, sessionID, gatewayruntime.SessionOptions{
			SessionID:        sessionID,
			Model:            model,
			SystemPrompt:     request.SystemPrompt,
			SystemPromptMode: systemMessageMode,
			ReasoningEffort:  request.ReasoningEffort,
			Agent:            agent,
			Interactive:      request.Copilot.Interactive,
			Tools:            runtimeTools,
			PermissionMode:   permissionMode,
		})
	}, s.provider.DeleteSession)
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
	imageAttachments, imageCleanup, err := s.materializeAttachments(ctx, latestTurn.Attachments)
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	extraAttachments, extraCleanup, err := s.materializeCopilotAttachments(ctx, request.Copilot.Attachments)
	if err != nil {
		if imageCleanup != nil {
			_ = imageCleanup()
		}
		_ = lease.Release()
		return nil, err
	}
	cleanup := combineCleanup(imageCleanup, extraCleanup)
	if err := s.validateMaterializedImageAttachments(ctx, model, imageAttachments); err != nil {
		if cleanup != nil {
			_ = cleanup()
		}
		_ = lease.Release()
		return nil, err
	}
	attachments := append(imageAttachments, extraAttachments...)

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
		includeRuntimeEvents: request.Copilot.IncludeRuntimeEvents || request.Copilot.Interactive || len(request.Copilot.Tools) > 0 || permissionMode == gatewayruntime.PermissionModeBridge,
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
	case errors.Is(err, errMissingSessionKey):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInvalidAnthropicVersion):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInvalidSystemMessageMode), errors.Is(err, errUnknownAgent):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errInteractiveRequiresStream), errors.Is(err, errInteractiveRequiresSessionKey), errors.Is(err, errInvalidCopilotPermissionMode), errors.Is(err, errCopilotPermissionEscalation), errors.Is(err, errInvalidCopilotTool), errors.Is(err, errInvalidCopilotResponse), errors.Is(err, errInvalidCopilotAttachment):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, errUnsupportedReasoningEffort), errors.Is(err, errUnsupportedVisionModel), errors.Is(err, errModelLimitsExceeded), errors.Is(err, errUnknownModelCapabilities):
		return http.StatusBadRequest, "invalid_request_error", err.Error()
	case errors.Is(err, session.ErrSessionSpecMismatch):
		return http.StatusConflict, "session_conflict", err.Error()
	case errors.Is(err, session.ErrTooManySessionsForNamespace):
		return http.StatusTooManyRequests, "session_limit_exceeded", err.Error()
	case errors.Is(err, session.ErrTooManySessions):
		return http.StatusServiceUnavailable, "session_capacity_exceeded", err.Error()
	case errors.Is(err, gatewayruntime.ErrPendingRequestNotFound):
		return http.StatusNotFound, "invalid_request_error", err.Error()
	case errors.Is(err, gatewayruntime.ErrPendingRequestsUnsupported):
		return http.StatusConflict, "session_conflict", err.Error()
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
	permissionMode, err := effectivePermissionMode(request.Copilot.PermissionMode, s.cfg.SDKPermissionMode)
	if err != nil {
		return err
	}
	if (request.Copilot.Interactive || len(request.Copilot.Tools) > 0 || permissionMode == gatewayruntime.PermissionModeBridge) && !request.Stream {
		return errInteractiveRequiresStream
	}
	if err := validateCopilotTools(request.Copilot.Tools); err != nil {
		return err
	}

	if err := compat.ValidateAttachmentPlacement(request.Turns, false); err != nil {
		return err
	}

	if request.ReasoningEffort == "" && attachmentCount(request.Turns) == 0 {
		return nil
	}

	model, err := s.lookupModel(ctx, modelID)
	if err != nil {
		return err
	}
	if model == nil {
		return unknownModelCapabilitiesError(modelID, request.ReasoningEffort != "", attachmentCount(request.Turns) > 0)
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
	for _, attachment := range latest.Attachments {
		if len(attachment.Data) == 0 {
			return nil
		}
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

func unknownModelCapabilitiesError(modelID string, hasReasoning, hasImages bool) error {
	features := make([]string, 0, 2)
	if hasReasoning {
		features = append(features, "reasoning_effort")
	}
	if hasImages {
		features = append(features, "image input")
	}
	if len(features) == 0 {
		return nil
	}
	return fmt.Errorf("%w: cannot validate %s for model %q; list the model in /v1/models first or retry without those features", errUnknownModelCapabilities, strings.Join(features, " and "), modelID)
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

func effectivePermissionMode(requestMode string, defaultMode gatewayruntime.PermissionMode) (gatewayruntime.PermissionMode, error) {
	if defaultMode == "" || defaultMode == gatewayruntime.PermissionModeInherit {
		defaultMode = gatewayruntime.PermissionModeDeny
	}
	switch normalized := gatewayruntime.PermissionMode(strings.ToLower(strings.TrimSpace(requestMode))); normalized {
	case "", gatewayruntime.PermissionModeInherit:
		return defaultMode, nil
	case gatewayruntime.PermissionModeAllow, gatewayruntime.PermissionModeDeny, gatewayruntime.PermissionModeBridge:
		if permissionModeRank(normalized) > permissionModeRank(defaultMode) {
			return "", fmt.Errorf("%w: requested=%s server=%s", errCopilotPermissionEscalation, normalized, defaultMode)
		}
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: %s", errInvalidCopilotPermissionMode, requestMode)
	}
}

func permissionModeRank(mode gatewayruntime.PermissionMode) int {
	switch mode {
	case gatewayruntime.PermissionModeAllow:
		return 2
	case gatewayruntime.PermissionModeBridge:
		return 1
	default:
		return 0
	}
}

func hasCopilotRequestExtension(extension compat.CopilotRequestExtension) bool {
	return extension.IncludeReasoning ||
		extension.IncludeRuntimeEvents ||
		extension.Interactive ||
		strings.TrimSpace(extension.PermissionMode) != "" ||
		len(extension.Attachments) > 0 ||
		len(extension.Tools) > 0 ||
		strings.TrimSpace(extension.Agent) != "" ||
		strings.TrimSpace(extension.SystemMessageMode) != ""
}

func toRuntimeTools(definitions []compat.CopilotToolDefinition) []gatewayruntime.ToolDefinition {
	if len(definitions) == 0 {
		return nil
	}
	tools := make([]gatewayruntime.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		var parameters map[string]any
		if definition.Parameters != nil {
			parameters = cloneAny(definition.Parameters).(map[string]any)
		}
		tools = append(tools, gatewayruntime.ToolDefinition{
			Name:            definition.Name,
			Description:     definition.Description,
			Parameters:      parameters,
			OverrideBuiltIn: definition.OverrideBuiltIn,
		})
	}
	return tools
}

func toolDefinitionsFingerprint(definitions []compat.CopilotToolDefinition) (string, error) {
	if len(definitions) == 0 {
		return "", nil
	}
	serialized := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		encoded, err := json.Marshal(definition)
		if err != nil {
			return "", err
		}
		serialized = append(serialized, string(encoded))
	}
	sort.Strings(serialized)
	encoded, err := json.Marshal(serialized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func validateCopilotTools(definitions []compat.CopilotToolDefinition) error {
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return fmt.Errorf("%w: name is required", errInvalidCopilotTool)
		}
		if definition.Parameters == nil {
			continue
		}
		if rawType, ok := definition.Parameters["type"].(string); ok && rawType != "" && rawType != "object" {
			return fmt.Errorf("%w: tool %s parameters.type must be object", errInvalidCopilotTool, definition.Name)
		}
	}
	return nil
}

func (s *Server) materializeAttachments(ctx context.Context, images []compat.ImageAttachment) ([]gatewayruntime.Attachment, func() error, error) {
	if len(images) == 0 {
		return nil, nil, nil
	}

	dir, err := os.MkdirTemp("", "copilot-sdkapi-images-*")
	if err != nil {
		return nil, nil, err
	}

	cleanup := func() error {
		if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	attachments := make([]gatewayruntime.Attachment, 0, len(images))
	for _, image := range images {
		mediaType := image.MediaType
		data := image.Data
		nameHint := "image"
		if strings.TrimSpace(image.URL) != "" {
			fetchedType, fetchedData, fetchedName, err := s.fetchRemoteAttachment(ctx, image.URL)
			if err != nil {
				_ = cleanup()
				return nil, nil, err
			}
			mediaType = fetchedType
			data = fetchedData
			nameHint = fetchedName
		}
		attachment, err := writeAttachmentFile(dir, nameHint, mediaType, data)
		if err != nil {
			_ = cleanup()
			return nil, nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, cleanup, nil
}

func (s *Server) materializeCopilotAttachments(ctx context.Context, inputs []compat.CopilotAttachment) ([]gatewayruntime.Attachment, func() error, error) {
	if len(inputs) == 0 {
		return nil, nil, nil
	}

	dir, err := os.MkdirTemp("", "copilot-sdkapi-files-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() error {
		if err := os.RemoveAll(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	attachments := make([]gatewayruntime.Attachment, 0, len(inputs))
	for _, input := range inputs {
		attachment, err := s.materializeCopilotAttachment(ctx, dir, input)
		if err != nil {
			_ = cleanup()
			return nil, nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, cleanup, nil
}

func (s *Server) materializeCopilotAttachment(ctx context.Context, dir string, input compat.CopilotAttachment) (gatewayruntime.Attachment, error) {
	nameHint := strings.TrimSpace(input.Name)
	if nameHint == "" {
		nameHint = "attachment"
	}
	mediaType := strings.TrimSpace(input.MediaType)
	switch {
	case strings.TrimSpace(input.Data) != "":
		decoded, err := base64.StdEncoding.DecodeString(input.Data)
		if err != nil {
			return gatewayruntime.Attachment{}, fmt.Errorf("%w: %v", errInvalidCopilotAttachment, err)
		}
		return writeAttachmentFile(dir, nameHint, mediaType, decoded)
	case strings.TrimSpace(input.Text) != "":
		if mediaType == "" {
			mediaType = "text/plain"
		}
		return writeAttachmentFile(dir, nameHint, mediaType, []byte(input.Text))
	case strings.TrimSpace(input.URL) != "":
		fetchedType, fetchedData, fetchedName, err := s.fetchRemoteAttachment(ctx, input.URL)
		if err != nil {
			return gatewayruntime.Attachment{}, err
		}
		if mediaType == "" {
			mediaType = fetchedType
		}
		if strings.TrimSpace(input.Name) == "" {
			nameHint = fetchedName
		}
		return writeAttachmentFile(dir, nameHint, mediaType, fetchedData)
	default:
		return gatewayruntime.Attachment{}, fmt.Errorf("%w: attachment must include data, text, or url", errInvalidCopilotAttachment)
	}
}

func (s *Server) fetchRemoteAttachment(ctx context.Context, rawURL string) (string, []byte, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", errInvalidCopilotAttachment, err)
	}
	if err := validateRemoteAttachmentURL(parsed, s.cfg.AllowPrivateRemoteURLs); err != nil {
		return "", nil, "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", nil, "", fmt.Errorf("%w: %v", errInvalidCopilotAttachment, err)
	}
	response, err := newRemoteFetchClient(s.cfg.AllowPrivateRemoteURLs).Do(request)
	if err != nil {
		return "", nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", nil, "", fmt.Errorf("%w: unexpected status %d", errInvalidCopilotAttachment, response.StatusCode)
	}

	limited := io.LimitReader(response.Body, s.cfg.MaxBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, "", err
	}
	if int64(len(data)) > s.cfg.MaxBodyBytes {
		return "", nil, "", fmt.Errorf("%w: remote attachment exceeds max body size", errInvalidCopilotAttachment)
	}
	mediaType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type")))
	if mediaType != "" {
		mediaType, _, _ = strings.Cut(mediaType, ";")
	}
	if mediaType == "" {
		mediaType = strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	}
	nameHint := path.Base(parsed.Path)
	if nameHint == "." || nameHint == "/" || strings.TrimSpace(nameHint) == "" {
		nameHint = "attachment"
	}
	return mediaType, data, nameHint, nil
}

func newRemoteFetchClient(allowPrivate bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		resolved, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		var blocked int
		for _, ip := range resolved {
			if !allowPrivate {
				if err := validateRemoteAttachmentIP(ip); err != nil {
					blocked++
					continue
				}
			}
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
		}
		if blocked == len(resolved) && blocked > 0 {
			return nil, fmt.Errorf("%w: private or special-use remote URLs are disabled", errInvalidCopilotAttachment)
		}
		return nil, fmt.Errorf("%w: unable to connect to remote attachment host", errInvalidCopilotAttachment)
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return validateRemoteAttachmentURL(req.URL, allowPrivate)
		},
	}
}

func validateRemoteAttachmentURL(parsed *url.URL, allowPrivate bool) error {
	if parsed == nil {
		return fmt.Errorf("%w: remote URL is required", errInvalidCopilotAttachment)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: unsupported URL scheme %s", errInvalidCopilotAttachment, parsed.Scheme)
	}
	hostname := strings.TrimSpace(parsed.Hostname())
	if hostname == "" {
		return fmt.Errorf("%w: remote URL host is required", errInvalidCopilotAttachment)
	}
	if allowPrivate {
		return nil
	}
	if isLocalhostHostname(hostname) {
		return fmt.Errorf("%w: private or special-use remote URLs are disabled", errInvalidCopilotAttachment)
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return validateRemoteAttachmentIP(ip)
	}
	return nil
}

func validateRemoteAttachmentIP(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("%w: invalid remote IP address", errInvalidCopilotAttachment)
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return fmt.Errorf("%w: invalid remote IP address", errInvalidCopilotAttachment)
	}
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() {
		return fmt.Errorf("%w: private or special-use remote URLs are disabled", errInvalidCopilotAttachment)
	}
	for _, prefix := range blockedRemoteIPPrefixes {
		if prefix.Contains(addr) {
			return fmt.Errorf("%w: private or special-use remote URLs are disabled", errInvalidCopilotAttachment)
		}
	}
	return nil
}

func isLocalhostHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func mustParsePrefix(raw string) netip.Prefix {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		panic(err)
	}
	return prefix
}

func writeAttachmentFile(dir, nameHint, mediaType string, data []byte) (gatewayruntime.Attachment, error) {
	if len(data) == 0 {
		return gatewayruntime.Attachment{}, fmt.Errorf("%w: attachment content must not be empty", errInvalidCopilotAttachment)
	}
	if strings.TrimSpace(mediaType) == "" {
		mediaType = strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	}
	pattern := sanitizeAttachmentName(nameHint) + "-*" + extensionForMediaType(mediaType)
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return gatewayruntime.Attachment{}, err
	}
	if _, err := file.Write(data); err != nil {
		name := file.Name()
		_ = file.Close()
		_ = os.Remove(name)
		return gatewayruntime.Attachment{}, err
	}
	if err := file.Close(); err != nil {
		name := file.Name()
		_ = os.Remove(name)
		return gatewayruntime.Attachment{}, err
	}
	return gatewayruntime.Attachment{
		Path:      file.Name(),
		MediaType: mediaType,
	}, nil
}

func (s *Server) validateMaterializedImageAttachments(ctx context.Context, modelID string, attachments []gatewayruntime.Attachment) error {
	if len(attachments) == 0 {
		return nil
	}
	model, err := s.lookupModel(ctx, modelID)
	if err != nil {
		return err
	}
	if model == nil {
		return unknownModelCapabilitiesError(modelID, false, true)
	}
	if model.Limits.Vision == nil {
		return nil
	}
	if len(model.Limits.Vision.SupportedMediaTypes) > 0 {
		allowed := make(map[string]struct{}, len(model.Limits.Vision.SupportedMediaTypes))
		for _, mediaType := range model.Limits.Vision.SupportedMediaTypes {
			allowed[strings.ToLower(strings.TrimSpace(mediaType))] = struct{}{}
		}
		for _, attachment := range attachments {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(attachment.MediaType))]; !ok {
				return fmt.Errorf("%w: unsupported media type %s", errModelLimitsExceeded, attachment.MediaType)
			}
		}
	}
	if limit := model.Limits.Vision.MaxPromptImageSize; limit > 0 {
		for _, attachment := range attachments {
			info, err := os.Stat(attachment.Path)
			if err != nil {
				return err
			}
			if info.Size() > int64(limit) {
				return fmt.Errorf("%w: image exceeds max_prompt_image_size", errModelLimitsExceeded)
			}
		}
	}
	return nil
}

func combineCleanup(cleanups ...func() error) func() error {
	filtered := make([]func() error, 0, len(cleanups))
	for _, cleanup := range cleanups {
		if cleanup != nil {
			filtered = append(filtered, cleanup)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return func() error {
		var firstErr error
		for _, cleanup := range filtered {
			if err := cleanup(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
}

func sanitizeAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	name = filepath.Base(name)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, "-")
	if name == "" {
		return "attachment"
	}
	return name
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
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "application/json":
		return ".json"
	case "application/pdf":
		return ".pdf"
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

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	touchCh := make(chan struct{}, 1)
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-touchCh:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				deadline := time.Unix(0, lastActivity.Load()).Add(idle)
				wait := time.Until(deadline)
				if wait <= 0 {
					wait = idle
				}
				timer.Reset(wait)
			case <-timer.C:
				deadline := time.Unix(0, lastActivity.Load()).Add(idle)
				if remaining := time.Until(deadline); remaining > 0 {
					timer.Reset(remaining)
					continue
				}
				cancel(errStreamIdleTimeout)
				return
			}
		}
	}()

	touch := func() {
		lastActivity.Store(time.Now().UnixNano())
		select {
		case touchCh <- struct{}{}:
		default:
		}
	}

	return ctx, cancel, touch
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
