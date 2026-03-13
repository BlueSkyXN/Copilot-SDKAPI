package compat

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

type OpenAIChatCompletionRequest struct {
	Model           string                   `json:"model"`
	Messages        []json.RawMessage        `json:"messages"`
	Stream          bool                     `json:"stream,omitempty"`
	MaxTokens       *int                     `json:"max_tokens,omitempty"`
	Temperature     *float64                 `json:"temperature,omitempty"`
	TopP            *float64                 `json:"top_p,omitempty"`
	ReasoningEffort string                   `json:"reasoning_effort,omitempty"`
	Tools           []json.RawMessage        `json:"tools,omitempty"`
	XCopilot        *CopilotRequestExtension `json:"x_copilot,omitempty"`
}

var openAIAllowedFields = map[string]struct{}{
	"model":            {},
	"messages":         {},
	"stream":           {},
	"max_tokens":       {},
	"temperature":      {},
	"top_p":            {},
	"reasoning_effort": {},
	"tools":            {},
	"x_copilot":        {},
}

type OpenAIMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type OpenAIChatCompletionResponse struct {
	ID       string                    `json:"id"`
	Object   string                    `json:"object"`
	Created  int64                     `json:"created"`
	Model    string                    `json:"model"`
	Choices  []OpenAIChoice            `json:"choices"`
	Usage    OpenAIUsage               `json:"usage"`
	XCopilot *CopilotResponseExtension `json:"x_copilot,omitempty"`
}

type OpenAIChoice struct {
	Index        int                   `json:"index"`
	Message      OpenAIResponseMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type OpenAIResponseMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type OpenAIChatCompletionChunk struct {
	ID       string                 `json:"id"`
	Object   string                 `json:"object"`
	Created  int64                  `json:"created"`
	Model    string                 `json:"model"`
	Choices  []OpenAIChunkChoice    `json:"choices"`
	XCopilot *OpenAIStreamExtension `json:"x_copilot,omitempty"`
}

type OpenAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type OpenAIStreamExtension struct {
	Event *gatewayruntime.RuntimeEvent `json:"event,omitempty"`
}

type OpenAIModelsResponse struct {
	Object string            `json:"object"`
	Data   []OpenAIModelInfo `json:"data"`
}

type OpenAIModelInfo struct {
	ID       string                `json:"id"`
	Object   string                `json:"object"`
	Created  int64                 `json:"created"`
	OwnedBy  string                `json:"owned_by"`
	XCopilot *OpenAIModelExtension `json:"x_copilot,omitempty"`
}

type OpenAIModelExtension struct {
	Supports                  gatewayruntime.ModelSupports `json:"supports,omitempty"`
	Limits                    gatewayruntime.ModelLimits   `json:"limits,omitempty"`
	SupportedReasoningEfforts []string                     `json:"supported_reasoning_efforts,omitempty"`
	DefaultReasoningEffort    string                       `json:"default_reasoning_effort,omitempty"`
}

func ParseOpenAIChatCompletionRequest(body io.Reader) (ConversationRequest, error) {
	payload, err := io.ReadAll(body)
	if err != nil {
		return ConversationRequest{}, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return ConversationRequest{}, fmt.Errorf("decode OpenAI request: %w", err)
	}
	for key := range raw {
		if _, ok := openAIAllowedFields[key]; !ok {
			return ConversationRequest{}, fmt.Errorf("%w: %s", ErrUnsupportedControls, key)
		}
	}

	var request OpenAIChatCompletionRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return ConversationRequest{}, fmt.Errorf("decode OpenAI request: %w", err)
	}
	if len(request.Tools) > 0 {
		return ConversationRequest{}, ErrUnsupportedTools
	}
	if request.MaxTokens != nil || request.Temperature != nil || request.TopP != nil {
		return ConversationRequest{}, ErrUnsupportedControls
	}
	if len(request.Messages) == 0 {
		return ConversationRequest{}, ErrNoTurns
	}

	var systemParts []string
	turns := make([]Turn, 0, len(request.Messages))
	for _, rawMessage := range request.Messages {
		role, content, attachments, err := parseOpenAIMessage(rawMessage)
		if err != nil {
			return ConversationRequest{}, err
		}
		if role == "system" {
			if len(attachments) > 0 {
				return ConversationRequest{}, ErrUnsupportedImages
			}
			if strings.TrimSpace(content) != "" {
				systemParts = append(systemParts, content)
			}
			continue
		}
		turns = append(turns, Turn{Role: role, Content: content, Attachments: attachments})
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
		SystemPrompt:    strings.TrimSpace(strings.Join(systemParts, "\n\n")),
		ReasoningEffort: strings.TrimSpace(request.ReasoningEffort),
		Stream:          request.Stream,
		Turns:           turns,
		Copilot:         ext,
	}, nil
}

func parseOpenAIMessage(raw json.RawMessage) (string, string, []ImageAttachment, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", nil, fmt.Errorf("decode OpenAI message: %w", err)
	}
	for key := range fields {
		switch key {
		case "role", "content":
		default:
			return "", "", nil, fmt.Errorf("%w: %s", ErrUnsupportedMessageFields, key)
		}
	}

	var message OpenAIMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", "", nil, fmt.Errorf("decode OpenAI message: %w", err)
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "system", "user", "assistant":
	default:
		return "", "", nil, fmt.Errorf("%w: role %s", ErrUnsupportedMessageFields, message.Role)
	}

	content, attachments, err := extractConversationContent(message.Content)
	if err != nil {
		return "", "", nil, err
	}
	return role, content, attachments, nil
}

func BuildOpenAIChatCompletionResponse(responseID, model, content string, usage gatewayruntime.Usage, extension *CopilotResponseExtension) OpenAIChatCompletionResponse {
	return OpenAIChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIResponseMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			TotalTokens:      usage.TotalTokens(),
		},
		XCopilot: extension,
	}
}

func BuildOpenAIStreamChunk(responseID, model string, delta OpenAIDelta, finishReason *string, extension *OpenAIStreamExtension) OpenAIChatCompletionChunk {
	return OpenAIChatCompletionChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChunkChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
		XCopilot: extension,
	}
}

func BuildOpenAIModelsResponse(models []gatewayruntime.Model) OpenAIModelsResponse {
	items := make([]OpenAIModelInfo, 0, len(models))
	now := time.Now().Unix()
	for _, model := range models {
		items = append(items, OpenAIModelInfo{
			ID:      model.ID,
			Object:  "model",
			Created: now,
			OwnedBy: "github-copilot",
			XCopilot: &OpenAIModelExtension{
				Supports:                  model.Supports,
				Limits:                    model.Limits,
				SupportedReasoningEfforts: append([]string(nil), model.SupportedReasoningEfforts...),
				DefaultReasoningEffort:    model.DefaultReasoningEffort,
			},
		})
	}
	return OpenAIModelsResponse{
		Object: "list",
		Data:   items,
	}
}
