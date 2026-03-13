package compat

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

type ClaudeMessagesRequest struct {
	Model           string                   `json:"model"`
	MaxTokens       *int                     `json:"max_tokens,omitempty"`
	System          any                      `json:"system,omitempty"`
	Messages        []json.RawMessage        `json:"messages"`
	Stream          bool                     `json:"stream,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
	Tools           []json.RawMessage        `json:"tools,omitempty"`
	XCopilot        *CopilotRequestExtension `json:"x_copilot,omitempty"`
}

var claudeAllowedFields = map[string]struct{}{
	"model":            {},
	"max_tokens":       {},
	"system":           {},
	"messages":         {},
	"stream":           {},
	"reasoning_effort": {},
	"tools":            {},
	"x_copilot":        {},
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type ClaudeMessageResponse struct {
	ID           string                    `json:"id"`
	Type         string                    `json:"type"`
	Role         string                    `json:"role"`
	Model        string                    `json:"model"`
	Content      []ClaudeTextBlock         `json:"content"`
	StopReason   string                    `json:"stop_reason,omitempty"`
	StopSequence *string                   `json:"stop_sequence"`
	Usage        ClaudeUsage               `json:"usage"`
	XCopilot     *CopilotResponseExtension `json:"x_copilot,omitempty"`
}

type ClaudeTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ClaudeUsage struct {
	InputTokens              int64 `json:"input_tokens,omitempty"`
	OutputTokens             int64 `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

func ParseClaudeMessagesRequest(body io.Reader) (ConversationRequest, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return ConversationRequest{}, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ConversationRequest{}, fmt.Errorf("decode Claude request: %w", err)
	}
	for key := range raw {
		if _, ok := claudeAllowedFields[key]; !ok {
			return ConversationRequest{}, fmt.Errorf("%w: %s", ErrUnsupportedControls, key)
		}
	}

	var request ClaudeMessagesRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return ConversationRequest{}, fmt.Errorf("decode Claude request: %w", err)
	}
	if len(request.Tools) > 0 {
		return ConversationRequest{}, ErrUnsupportedTools
	}
	if request.MaxTokens != nil {
		return ConversationRequest{}, ErrUnsupportedControls
	}
	if len(request.Messages) == 0 {
		return ConversationRequest{}, ErrNoTurns
	}

	systemPrompt, err := extractTextContent(request.System)
	if err != nil {
		return ConversationRequest{}, err
	}

	turns := make([]Turn, 0, len(request.Messages))
	for _, rawMessage := range request.Messages {
		role, content, attachments, err := parseClaudeMessage(rawMessage)
		if err != nil {
			return ConversationRequest{}, err
		}
		turns = append(turns, Turn{
			Role:        role,
			Content:     content,
			Attachments: attachments,
		})
	}
	if _, err := LatestUserPrompt(turns); err != nil {
		return ConversationRequest{}, err
	}
	if err := ValidateAttachmentPlacement(turns, true); err != nil {
		return ConversationRequest{}, err
	}

	ext := CopilotRequestExtension{}
	if request.XCopilot != nil {
		ext = *request.XCopilot
	}
	return ConversationRequest{
		Model:           request.Model,
		SystemPrompt:    strings.TrimSpace(systemPrompt),
		ReasoningEffort: strings.TrimSpace(request.ReasoningEffort),
		Stream:          request.Stream,
		Turns:           turns,
		Copilot:         ext,
	}, nil
}

func parseClaudeMessage(raw json.RawMessage) (string, string, []ImageAttachment, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", nil, fmt.Errorf("decode Claude message: %w", err)
	}
	for key := range fields {
		switch key {
		case "role", "content":
		default:
			return "", "", nil, fmt.Errorf("%w: %s", ErrUnsupportedMessageFields, key)
		}
	}

	var message ClaudeMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", "", nil, fmt.Errorf("decode Claude message: %w", err)
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "user", "assistant":
	default:
		return "", "", nil, fmt.Errorf("%w: role %s", ErrUnsupportedMessageFields, message.Role)
	}

	content, attachments, err := extractConversationContent(message.Content)
	if err != nil {
		return "", "", nil, err
	}
	return role, content, attachments, nil
}

func BuildClaudeMessageResponse(responseID, model, content string, usage gatewayruntime.Usage, extension *CopilotResponseExtension) ClaudeMessageResponse {
	return ClaudeMessageResponse{
		ID:    responseID,
		Type:  "message",
		Role:  "assistant",
		Model: model,
		Content: []ClaudeTextBlock{
			{
				Type: "text",
				Text: content,
			},
		},
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage: ClaudeUsage{
			InputTokens:              usage.InputTokens,
			OutputTokens:             usage.OutputTokens,
			CacheCreationInputTokens: usage.CacheWriteTokens,
			CacheReadInputTokens:     usage.CacheReadTokens,
		},
		XCopilot: extension,
	}
}
