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
	Models              []string                 `json:"models,omitempty"`
	Messages            []json.RawMessage        `json:"messages"`
	Prompt              string                   `json:"prompt,omitempty"`
	Stream              bool                     `json:"stream,omitempty"`
	MaxTokens           *int                     `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int                     `json:"max_completion_tokens,omitempty"`
	Temperature         *float64                 `json:"temperature,omitempty"`
	TopP                *float64                 `json:"top_p,omitempty"`
	PresencePenalty     *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty    *float64                 `json:"frequency_penalty,omitempty"`
	TopK                *int                     `json:"top_k,omitempty"`
	RepetitionPenalty   *float64                 `json:"repetition_penalty,omitempty"`
	MinP                *float64                 `json:"min_p,omitempty"`
	TopA                *float64                 `json:"top_a,omitempty"`
	Seed                *int64                   `json:"seed,omitempty"`
	N                   *int                     `json:"n,omitempty"`
	Stop                any                      `json:"stop,omitempty"`
	User                string                   `json:"user,omitempty"`
	ServiceTier         string                   `json:"service_tier,omitempty"`
	Store               *bool                    `json:"store,omitempty"`
	ResponseFormat      json.RawMessage          `json:"response_format,omitempty"`
	StreamOptions       json.RawMessage          `json:"stream_options,omitempty"`
	Metadata            json.RawMessage          `json:"metadata,omitempty"`
	LogitBias           json.RawMessage          `json:"logit_bias,omitempty"`
	Logprobs            *bool                    `json:"logprobs,omitempty"`
	TopLogprobs         *int                     `json:"top_logprobs,omitempty"`
	Modalities          []string                 `json:"modalities,omitempty"`
	Audio               json.RawMessage          `json:"audio,omitempty"`
	ReasoningEffort     string                   `json:"reasoning_effort,omitempty"`
	Reasoning           json.RawMessage          `json:"reasoning,omitempty"`
	Tools               []json.RawMessage        `json:"tools,omitempty"`
	ToolChoice          json.RawMessage          `json:"tool_choice,omitempty"`
	ParallelToolCalls   *bool                    `json:"parallel_tool_calls,omitempty"`
	Functions           []json.RawMessage        `json:"functions,omitempty"`
	FunctionCall        json.RawMessage          `json:"function_call,omitempty"`
	Plugins             []json.RawMessage        `json:"plugins,omitempty"`
	Provider            json.RawMessage          `json:"provider,omitempty"`
	Route               string                   `json:"route,omitempty"`
	Transforms          []string                 `json:"transforms,omitempty"`
	Prediction          json.RawMessage          `json:"prediction,omitempty"`
	StructuredOutputs   *bool                    `json:"structured_outputs,omitempty"`
	AdaptiveThinking    json.RawMessage          `json:"adaptive_thinking,omitempty"`
	ThinkingBudget      json.RawMessage          `json:"thinking_budget,omitempty"`
	Verbosity           string                   `json:"verbosity,omitempty"`
	Debug               json.RawMessage          `json:"debug,omitempty"`
	XCopilot            *CopilotRequestExtension `json:"x_copilot,omitempty"`
}

type OpenAIMessage struct {
	Role             string          `json:"role"`
	Content          any             `json:"content"`
	Name             string          `json:"name,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	FunctionCall     json.RawMessage `json:"function_call,omitempty"`
	Refusal          string          `json:"refusal,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
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

type OpenAIResponseFormat struct {
	Type       string                    `json:"type,omitempty"`
	JSONSchema *OpenAIResponseJSONSchema `json:"json_schema,omitempty"`
}

type OpenAIResponseJSONSchema struct {
	Name   string          `json:"name,omitempty"`
	Strict *bool           `json:"strict,omitempty"`
	Schema json.RawMessage `json:"schema,omitempty"`
}

type OpenAIReasoningConfig struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
	Exclude   *bool  `json:"exclude,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

type OpenAIStreamOptions struct {
	IncludeUsage *bool `json:"include_usage,omitempty"`
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
	Role      string `json:"role"`
	Content   string `json:"content"`
	Reasoning string `json:"reasoning,omitempty"`
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
	Usage    *OpenAIUsage           `json:"usage,omitempty"`
	XCopilot *OpenAIStreamExtension `json:"x_copilot,omitempty"`
}

type OpenAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type OpenAIDelta struct {
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
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
	if request.N != nil && *request.N != 1 {
		return ConversationRequest{}, fmt.Errorf("%w: only n=1 is supported", ErrUnsupportedControls)
	}
	if requestsOpenAIAudioOutput(request.Modalities, request.Audio) {
		return ConversationRequest{}, fmt.Errorf("%w: audio output is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if request.StructuredOutputs != nil && *request.StructuredOutputs {
		return ConversationRequest{}, fmt.Errorf("%w: structured_outputs strict enforcement is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if hasNonNullJSON(request.AdaptiveThinking) {
		return ConversationRequest{}, fmt.Errorf("%w: adaptive_thinking is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if hasNonNullJSON(request.ThinkingBudget) {
		return ConversationRequest{}, fmt.Errorf("%w: thinking_budget is not supported by this gateway yet", ErrUnsupportedControls)
	}

	ext := CopilotRequestExtension{}
	if request.XCopilot != nil {
		ext = *request.XCopilot
	}
	reasoningEffort, err := parseOpenAIReasoningEffort(request.ReasoningEffort, request.Reasoning)
	if err != nil {
		return ConversationRequest{}, err
	}
	includeUsageInStream, err := parseOpenAIStreamIncludeUsage(request.StreamOptions)
	if err != nil {
		return ConversationRequest{}, err
	}
	toolDirective, err := parseOpenAIToolDirective(request.ToolChoice, request.FunctionCall)
	if err != nil {
		return ConversationRequest{}, err
	}
	standardTools, err := mapOpenAICompatibleTools(request.Tools, request.Functions)
	if err != nil {
		return ConversationRequest{}, err
	}
	if toolDirective.DisableAll {
		standardTools = nil
	} else if toolDirective.RequiredToolName != "" {
		// OpenAI tool_choice only constrains standard OpenAI-compatible tools.
		// Native x_copilot.tools remain extension-specific and are intentionally not filtered here.
		if len(standardTools) == 0 {
			if len(filterCopilotToolsByName(ext.Tools, toolDirective.RequiredToolName)) == 0 {
				return ConversationRequest{}, fmt.Errorf("%w: requested tool %s was not provided", ErrUnsupportedTools, toolDirective.RequiredToolName)
			}
		}
		if len(standardTools) > 0 {
			filtered := filterCopilotToolsByName(standardTools, toolDirective.RequiredToolName)
			if len(filtered) == 0 {
				return ConversationRequest{}, fmt.Errorf("%w: requested tool %s was not provided", ErrUnsupportedTools, toolDirective.RequiredToolName)
			}
			standardTools = filtered
		}
	}
	if request.ParallelToolCalls != nil && *request.ParallelToolCalls && len(standardTools) > 0 {
		return ConversationRequest{}, fmt.Errorf("%w: parallel_tool_calls is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if len(standardTools) > 0 {
		ext.Tools = append(append([]CopilotToolDefinition(nil), standardTools...), ext.Tools...)
	}

	var systemParts []string
	responseFormatInstruction, err := buildOpenAIResponseFormatInstruction(request.ResponseFormat)
	if err != nil {
		return ConversationRequest{}, err
	}
	turns := make([]Turn, 0, maxInt(len(request.Messages), 1))
	if len(request.Messages) == 0 {
		if strings.TrimSpace(request.Prompt) == "" {
			return ConversationRequest{}, ErrNoTurns
		}
		turns = append(turns, Turn{Role: "user", Content: request.Prompt})
	} else {
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
	}
	if responseFormatInstruction != "" {
		systemParts = append(systemParts, responseFormatInstruction)
	}
	if _, err := LatestUserPrompt(turns); err != nil {
		return ConversationRequest{}, err
	}
	if err := ValidateAttachmentPlacement(turns, true); err != nil {
		return ConversationRequest{}, err
	}

	return ConversationRequest{
		Model:                effectiveOpenAIModel(request),
		SystemPrompt:         strings.TrimSpace(strings.Join(systemParts, "\n\n")),
		ReasoningEffort:      reasoningEffort,
		Stream:               request.Stream,
		IncludeUsageInStream: includeUsageInStream,
		Turns:                turns,
		Copilot:              ext,
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
	return role, renderOpenAIMessageContent(role, content, message.Name, message.ToolCalls, message.FunctionCall, message.ToolCallID, message.Refusal, message.Reasoning, message.ReasoningContent, message.ReasoningDetails), attachments, nil
}

func mapOpenAICompatibleTools(rawTools, rawFunctions []json.RawMessage) ([]CopilotToolDefinition, error) {
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

type openAIToolDirective struct {
	DisableAll       bool
	RequiredToolName string
}

func parseOpenAIToolDirective(rawToolChoice, rawFunctionCall json.RawMessage) (openAIToolDirective, error) {
	directive, err := parseSingleOpenAIToolDirective(rawToolChoice)
	if err != nil {
		return openAIToolDirective{}, err
	}
	if directive.DisableAll || directive.RequiredToolName != "" {
		return directive, nil
	}
	return parseSingleOpenAIToolDirective(rawFunctionCall)
}

func parseSingleOpenAIToolDirective(raw json.RawMessage) (openAIToolDirective, error) {
	if !hasNonNullJSON(raw) {
		return openAIToolDirective{}, nil
	}

	var literal string
	if err := json.Unmarshal(raw, &literal); err == nil {
		if strings.EqualFold(strings.TrimSpace(literal), "none") {
			return openAIToolDirective{DisableAll: true}, nil
		}
		return openAIToolDirective{}, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return openAIToolDirective{}, fmt.Errorf("%w: tool_choice/function_call must be a string or object", ErrUnsupportedControls)
	}
	if len(fields) == 0 {
		return openAIToolDirective{}, nil
	}

	var typed struct {
		Type     string `json:"type"`
		Name     string `json:"name,omitempty"`
		Function *struct {
			Name string `json:"name,omitempty"`
		} `json:"function,omitempty"`
	}
	if err := json.Unmarshal(raw, &typed); err != nil {
		return openAIToolDirective{}, fmt.Errorf("%w: tool_choice/function_call must use OpenAI-compatible string fields", ErrUnsupportedControls)
	}
	if strings.EqualFold(strings.TrimSpace(typed.Type), "none") {
		return openAIToolDirective{DisableAll: true}, nil
	}
	if typed.Function != nil {
		if name := strings.TrimSpace(typed.Function.Name); name != "" {
			return openAIToolDirective{RequiredToolName: name}, nil
		}
		return openAIToolDirective{}, fmt.Errorf("%w: tool_choice/function_call function selection requires a non-empty function name", ErrUnsupportedControls)
	}
	if _, ok := fields["name"]; ok {
		if name := strings.TrimSpace(typed.Name); name != "" {
			return openAIToolDirective{RequiredToolName: name}, nil
		}
		return openAIToolDirective{}, fmt.Errorf("%w: tool_choice/function_call name selection requires a non-empty function name", ErrUnsupportedControls)
	}
	if strings.EqualFold(strings.TrimSpace(typed.Type), "function") {
		return openAIToolDirective{}, fmt.Errorf("%w: tool_choice/function_call type=function requires a function name", ErrUnsupportedControls)
	}
	return openAIToolDirective{}, nil
}

func filterCopilotToolsByName(tools []CopilotToolDefinition, name string) []CopilotToolDefinition {
	name = strings.TrimSpace(name)
	if name == "" {
		return append([]CopilotToolDefinition(nil), tools...)
	}

	filtered := make([]CopilotToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == name {
			filtered = append(filtered, tool)
		}
	}
	return filtered
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

func parseOpenAIReasoningEffort(topLevel string, raw json.RawMessage) (string, error) {
	effort := strings.TrimSpace(topLevel)
	if !hasNonNullJSON(raw) {
		return effort, nil
	}

	var config OpenAIReasoningConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return "", fmt.Errorf("%w: reasoning must be an object", ErrUnsupportedControls)
	}
	if config.MaxTokens != nil {
		return "", fmt.Errorf("%w: reasoning.max_tokens is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if config.Exclude != nil {
		return "", fmt.Errorf("%w: reasoning.exclude is not supported by this gateway yet", ErrUnsupportedControls)
	}
	if config.Enabled != nil {
		if !*config.Enabled {
			if effort != "" || strings.TrimSpace(config.Effort) != "" {
				return "", fmt.Errorf("%w: reasoning.enabled=false conflicts with reasoning_effort settings", ErrUnsupportedControls)
			}
			return "", nil
		}
	}
	if effort != "" {
		return effort, nil
	}
	if config.Enabled != nil && *config.Enabled && strings.TrimSpace(config.Effort) == "" {
		return "medium", nil
	}
	return strings.TrimSpace(config.Effort), nil
}

func parseOpenAIStreamIncludeUsage(raw json.RawMessage) (bool, error) {
	if !hasNonNullJSON(raw) {
		return false, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false, fmt.Errorf("%w: stream_options must be an object", ErrUnsupportedControls)
	}
	value, ok := fields["include_usage"]
	if !ok || !hasNonNullJSON(value) {
		return false, nil
	}

	var includeUsage bool
	if err := json.Unmarshal(value, &includeUsage); err != nil {
		return false, fmt.Errorf("%w: stream_options.include_usage must be a boolean", ErrUnsupportedControls)
	}
	return includeUsage, nil
}

func hasNonNullJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

func renderOpenAIMessageContent(role, content, name string, toolCalls, functionCall json.RawMessage, toolCallID, refusal, reasoning, reasoningContent string, reasoningDetails json.RawMessage) string {
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
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		reasoning = strings.TrimSpace(reasoningContent)
	}
	if reasoning != "" {
		parts = append(parts, "Reasoning:\n"+reasoning)
	}
	if compact := compactJSON(reasoningDetails); compact != "" {
		parts = append(parts, "Reasoning details:\n"+compact)
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

func effectiveOpenAIModel(request OpenAIChatCompletionRequest) string {
	if model := strings.TrimSpace(request.Model); model != "" {
		return model
	}
	for _, candidate := range request.Models {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func buildOpenAIResponseFormatInstruction(raw json.RawMessage) (string, error) {
	if !hasNonNullJSON(raw) {
		return "", nil
	}

	var literal string
	if err := json.Unmarshal(raw, &literal); err == nil {
		switch strings.ToLower(strings.TrimSpace(literal)) {
		case "json_object":
			return "Return only valid JSON. Do not wrap it in markdown fences or surrounding prose.", nil
		default:
			return "", nil
		}
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", fmt.Errorf("%w: response_format must be a string or object", ErrUnsupportedControls)
	}

	var format OpenAIResponseFormat
	if err := json.Unmarshal(raw, &format); err != nil {
		return "", fmt.Errorf("%w: response_format must use OpenAI-compatible field types", ErrUnsupportedControls)
	}

	switch strings.ToLower(strings.TrimSpace(format.Type)) {
	case "json_object":
		return "Return only valid JSON. Do not wrap it in markdown fences or surrounding prose.", nil
	case "json_schema":
		parts := []string{
			"Return only valid JSON. Do not wrap it in markdown fences or surrounding prose.",
		}
		if format.JSONSchema != nil {
			if format.JSONSchema.Strict != nil && *format.JSONSchema.Strict {
				return "", fmt.Errorf("%w: response_format json_schema strict enforcement is not supported by this gateway yet", ErrUnsupportedControls)
			}
			if name := strings.TrimSpace(format.JSONSchema.Name); name != "" {
				parts = append(parts, fmt.Sprintf("JSON schema name: %s", name))
			}
			if schema := compactJSON(format.JSONSchema.Schema); schema != "" {
				parts = append(parts, "Match this JSON Schema:\n"+schema)
			}
		}
		return joinPromptParts(parts...), nil
	default:
		return "", nil
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func BuildOpenAIChatCompletionResponse(responseID, model, content, reasoning string, usage gatewayruntime.Usage, extension *CopilotResponseExtension) OpenAIChatCompletionResponse {
	reasoning = strings.TrimSpace(reasoning)
	return OpenAIChatCompletionResponse{
		ID:      responseID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIResponseMessage{
					Role:      "assistant",
					Content:   content,
					Reasoning: reasoning,
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

func BuildOpenAIUsageChunk(responseID, model string, usage gatewayruntime.Usage) OpenAIChatCompletionChunk {
	return OpenAIChatCompletionChunk{
		ID:      responseID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []OpenAIChunkChoice{},
		Usage: &OpenAIUsage{
			PromptTokens:     usage.InputTokens,
			CompletionTokens: usage.OutputTokens,
			TotalTokens:      usage.TotalTokens(),
		},
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
