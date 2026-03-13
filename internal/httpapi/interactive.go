package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

type copilotContinuationRequest struct {
	RequestID     string                            `json:"request_id"`
	Kind          string                            `json:"kind"`
	TextResult    string                            `json:"text_result,omitempty"`
	ResultType    string                            `json:"result_type,omitempty"`
	SessionLog    string                            `json:"session_log,omitempty"`
	Telemetry     map[string]any                    `json:"telemetry,omitempty"`
	BinaryResults []gatewayruntime.ToolBinaryResult `json:"binary_results,omitempty"`
	Error         string                            `json:"error,omitempty"`
	Answer        string                            `json:"answer,omitempty"`
	WasFreeform   bool                              `json:"was_freeform,omitempty"`
	Rules         []any                             `json:"rules,omitempty"`
}

func (s *Server) handleCopilotResponse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	identity, err := s.auth.AuthenticateRequest(r)
	if err != nil {
		s.writeOpenAIError(w, http.StatusUnauthorized, "authentication_error", "invalid API key")
		return
	}

	publicSessionKey := sessionKeyFromRequest(r)
	if publicSessionKey == "" {
		status, errorType, message := classifyError(errMissingSessionKey)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}
	if err := validateSessionKey(publicSessionKey); err != nil {
		status, errorType, message := classifyError(err)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}

	session := s.sessions.Lookup(scopedSessionKey(identity, publicSessionKey))
	if session == nil {
		status, errorType, message := classifyError(gatewayruntime.ErrPendingRequestNotFound)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}

	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	response, err := parseCopilotContinuationRequest(body)
	if err != nil {
		status, errorType, message := classifyError(err)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()
	if err := session.ResolvePending(ctx, response); err != nil {
		status, errorType, message := classifyError(err)
		s.writeOpenAIError(w, status, errorType, message)
		return
	}
	s.sessions.TouchActivity(scopedSessionKey(identity, publicSessionKey))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"request_id": response.RequestID,
	})
}

func parseCopilotContinuationRequest(body io.Reader) (gatewayruntime.PendingResponse, error) {
	var request copilotContinuationRequest
	if err := json.NewDecoder(body).Decode(&request); err != nil {
		return gatewayruntime.PendingResponse{}, fmt.Errorf("decode x_copilot continuation request: %w", err)
	}

	requestID := strings.TrimSpace(request.RequestID)
	if requestID == "" {
		return gatewayruntime.PendingResponse{}, fmt.Errorf("%w: request_id is required", errInvalidCopilotResponse)
	}

	switch strings.ToLower(strings.TrimSpace(request.Kind)) {
	case "tool_result":
		var telemetry map[string]any
		if request.Telemetry != nil {
			telemetry = cloneAny(request.Telemetry).(map[string]any)
		}
		return gatewayruntime.PendingResponse{
			RequestID: requestID,
			Kind:      gatewayruntime.PendingRequestTool,
			ToolResult: &gatewayruntime.ToolResult{
				TextResult:    request.TextResult,
				BinaryResults: append([]gatewayruntime.ToolBinaryResult(nil), request.BinaryResults...),
				ResultType:    request.ResultType,
				SessionLog:    request.SessionLog,
				Telemetry:     telemetry,
			},
		}, nil
	case "tool_error":
		if strings.TrimSpace(request.Error) == "" {
			return gatewayruntime.PendingResponse{}, fmt.Errorf("%w: tool_error requires error", errInvalidCopilotResponse)
		}
		return gatewayruntime.PendingResponse{
			RequestID: requestID,
			Kind:      gatewayruntime.PendingRequestTool,
			ToolError: request.Error,
		}, nil
	case "user_input":
		return gatewayruntime.PendingResponse{
			RequestID: requestID,
			Kind:      gatewayruntime.PendingRequestUserInput,
			UserInput: &gatewayruntime.UserInputResponse{
				Answer:      request.Answer,
				WasFreeform: request.WasFreeform,
			},
		}, nil
	case "permission_result":
		resultKind := strings.TrimSpace(request.ResultType)
		if resultKind == "" {
			return gatewayruntime.PendingResponse{}, fmt.Errorf("%w: permission_result requires result_type", errInvalidCopilotResponse)
		}
		if !isSupportedPermissionResultKind(resultKind) {
			return gatewayruntime.PendingResponse{}, fmt.Errorf("%w: unsupported permission result_type %s", errInvalidCopilotResponse, resultKind)
		}
		return gatewayruntime.PendingResponse{
			RequestID: requestID,
			Kind:      gatewayruntime.PendingRequestPermission,
			Permission: &gatewayruntime.PermissionResponse{
				ResultKind: resultKind,
				Rules:      append([]any(nil), request.Rules...),
			},
		}, nil
	default:
		return gatewayruntime.PendingResponse{}, fmt.Errorf("%w: unsupported kind %s", errInvalidCopilotResponse, request.Kind)
	}
}

func isSupportedPermissionResultKind(value string) bool {
	switch strings.TrimSpace(value) {
	case "approved",
		"denied-by-rules",
		"denied-interactively-by-user",
		"denied-no-approval-rule-and-could-not-request-from-user",
		"denied-by-content-exclusion-policy":
		return true
	default:
		return false
	}
}
