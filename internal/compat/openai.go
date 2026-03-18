package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

type OpenAIChatCompletionRequest struct {
	Model               string                   `json:"model"`
	Messages            []json.RawMessage        `json:"messages"`
	Stream              bool                     `json:"stream,omitempty"`
	MaxTokens           *int                     `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                     `json:"max_completion_tokens,omitempty"`
	Temperature         *float64                 `json:"temperature,omitempty"`
	TopP                *float64                 `json:"top_p,omitempty"`
	PresencePenalty     *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64                 `json:"frequency_penalty,omitempty"`
	N                   *int                     `json:"n,omitempty"`
	Stop                any                      `json:"stop,omitempty"`
	User                string                   `json:"user,omitempty"`
	ServiceTier         string                   `json:"service_tier,omitempty"`
	Store               *bool                    `json:"store,omitempty"`
	ResponseFormat      json.RawMessage          `json:"response_format,omitempty"`
	StreamOptions       json.RawMessage          `json:"stream_options,omitempty"`
	Metadata            json.RawMessage          `json:"metadata,omitempty"`
	Modalities          []string                 `json:"modalities,omitempty"`
	Audio               json.RawMessage          `json:"audio,omitempty"`
	ReasoningEffort     string                   `json:"reasoning_effort,omitempty"`
	Tools               []json.RawMessage        `json:"tools,omitempty"`
	ToolChoice          json.RawMessage          `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                    `json:"parallel_tool_calls,omitempty"`
	Functions           []json.RawMessage        `json:"functions,omitempty"`
	FunctionCall        json.RawMessage          `json:"function_call,omitempty"`
	XCopilot            *CopilotRequestExtension `json:"x_copilot,omitempty"`
}

type OpenAIMessage struct {
	Role         string          `json:"role"`
	Content      any             `json:"content"`
	Name         string          `json:"name,omitempty"`
	ToolCalls    json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID   string          `json:"tool_call_id,omitempty"`
	FunctionCall json.RawMessage `json:"function_call,omitempty"`
	Refusal      string          `json:"refusal,omitempty"`
}

type OpenAIChatTool struct {
	Type     string                    `json:"type,omitempty"`
	Function *OpenAIFunctionDefinition `json:"function,omitempty"`
}

type OpenAIFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
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

	var request OpenAIChatCompletionRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return ConversationRequest{}, fmt.Errorf("decode OpenAI request: %w", err)
	}
	if len(request.Messages) == 0 {
		return ConversationRequest{}, ErrNoTurns
	}
	if request.N != nil && *request.N != 1 {
		return ConversationRequest{}, fmt.Errorf("%w: only n=1 is supported", ErrUnsupportedControls)
	}
	if requestsOpenAIAudioOutput(request.Modalities, request.Audio) {
		return ConversationRequest{}, fmt.Errorf("%w: audio output is not supported by this gateway yet", ErrUnsupportedControls)
	}

	ext := CopilotRequestExtension{}
	if request.XCopilot != nil {
		ext = *request.XCopilot
	}
	standardTools, err := mapOpenAICompatibleTools(request.Tools, request.Functions, request.ToolChoice, request.FunctionCall)
	if err != nil {
		return ConversationRequest{}, err
	}
	if len(standardTools) > 0 {
		ext.Tools = append(append([]CopilotToolDefinition(nil), standardTools...), ext.Tools...)
	}

	var systemParts []string
	turns := make([]Turn, 0, len(request.Messages))
	for _, rawMessage := range request.Messages {
		role, content, attachments, err := parseOpenAIMessage(rawMessage)
		if err != nil {
			return ConversationRequest{}, err
		}
		if role == "system" || role == "developer" {
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
	var message OpenAIMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return "", "", nil, fmt.Errorf("decode OpenAI message: %w", err)
	}

	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "system", "developer", "user", "assistant", "tool":
	default:
		return "", "", nil, fmt.Errorf("%w: role %s", ErrUnsupportedMessageFields, message.Role)
	}

	content, attachments, err := extractConversationContent(message.Content)
	if err != nil {
		return "", "", nil, err
	}
	return role, renderOpenAIMessageContent(role, content, message.Name, message.ToolCalls, message.FunctionCall, message.ToolCallID, message.Refusal), attachments, nil
}

func mapOpenAICompatibleTools(rawTools, rawFunctions []json.RawMessage, rawToolChoice, rawFunctionCall json.RawMessage) ([]CopilotToolDefinition, error) {
	if disablesOpenAITools(rawToolChoice) || disablesOpenAITools(rawFunctionCall) {
		return nil, nil
	}

	tools := make([]CopilotToolDefinition, 0, len(rawTools)+len(rawFunctions))
	convertedTools, err := parseOpenAIChatTools(rawTools)
	if err != nil {
		return nil, err
	}
	tools = append(tools, convertedTools...)

	convertedFunctions, err := parseOpenAIFunctionDefinitions(rawFunctions)
	if err != nil {
		return nil, err
	}
	tools = append(tools, convertedFunctions...)
	return tools, nil
}

func parseOpenAIChatTools(rawTools []json.RawMessage) ([]CopilotToolDefinition, error) {
	tools := make([]CopilotToolDefinition, 0, len(rawTools))
	for _, rawTool := range rawTools {
		var tool OpenAIChatTool
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			return nil, fmt.Errorf("decode OpenAI tool: %w", err)
		}
		toolType := strings.ToLower(strings.TrimSpace(tool.Type))
		if toolType == "" {
			toolType = "function"
		}
		if toolType != "function" {
			return nil, fmt.Errorf("%w: OpenAI tool type %s", ErrUnsupportedTools, tool.Type)
		}
		if tool.Function == nil {
			return nil, fmt.Errorf("%w: OpenAI function tool is missing function metadata", ErrUnsupportedTools)
		}
		tools = append(tools, functionDefinitionToCopilotTool(tool.Function))
	}
	return tools, nil
}

func parseOpenAIFunctionDefinitions(rawFunctions []json.RawMessage) ([]CopilotToolDefinition, error) {
	tools := make([]CopilotToolDefinition, 0, len(rawFunctions))
	for _, rawFunction := range rawFunctions {
		var function OpenAIFunctionDefinition
		if err := json.Unmarshal(rawFunction, &function); err != nil {
			return nil, fmt.Errorf("decode OpenAI function: %w", err)
		}
		tools = append(tools, functionDefinitionToCopilotTool(&function))
	}
	return tools, nil
}

func functionDefinitionToCopilotTool(function *OpenAIFunctionDefinition) CopilotToolDefinition {
	if function == nil {
		return CopilotToolDefinition{}
	}
	return CopilotToolDefinition{
		Name:        strings.TrimSpace(function.Name),
		Description: strings.TrimSpace(function.Description),
		Parameters:  function.Parameters,
	}
}

func disablesOpenAITools(raw json.RawMessage) bool {
	if !hasNonNullJSON(raw) {
		return false
	}

	var literal string
	if err := json.Unmarshal(raw, &literal); err == nil {
		return strings.EqualFold(strings.TrimSpace(literal), "none")
	}

	var typed struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &typed); err == nil {
		return strings.EqualFold(strings.TrimSpace(typed.Type), "none")
	}
	return false
}

func requestsOpenAIAudioOutput(modalities []string, audio json.RawMessage) bool {
	if hasNonNullJSON(audio) {
		return true
	}
	for _, modality := range modalities {
		normalized := strings.ToLower(strings.TrimSpace(modality))
		if normalized != "" && normalized != "text" {
			return true
		}
	}
	return false
}

func hasNonNullJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func renderOpenAIMessageContent(role, content, name string, toolCalls, functionCall json.RawMessage, toolCallID, refusal string) string {
	parts := make([]string, 0, 6)

	name = strings.TrimSpace(name)
	if name != "" {
		label := "Name"
		if role == "tool" {
			label = "Tool name"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", label, name))
	}

	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID != "" {
		parts = append(parts, fmt.Sprintf("Tool call ID: %s", toolCallID))
	}

	content = strings.TrimSpace(content)
	if content != "" {
		if role == "tool" {
			parts = append(parts, "Result:\n"+content)
		} else {
			parts = append(parts, content)
		}
	}

	refusal = strings.TrimSpace(refusal)
	if refusal != "" {
		parts = append(parts, fmt.Sprintf("Refusal: %s", refusal))
	}

	if compact := compactJSON(toolCalls); compact != "" {
		parts = append(parts, "Tool calls:\n"+compact)
	}
	if compact := compactJSON(functionCall); compact != "" {
		parts = append(parts, "Function call:\n"+compact)
	}
	return joinPromptParts(parts...)
}

func compactJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		return trimmed
	}
	return buffer.String()
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
