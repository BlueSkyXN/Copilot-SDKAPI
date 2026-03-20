package compat

import (
	"strings"
	"testing"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=="

func TestParseOpenAIChatCompletionRequest(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"system","content":"Be brief."},
			{"role":"user","content":"Hello"},
			{"role":"assistant","content":"Hi"},
			{"role":"user","content":[{"type":"text","text":"How are you?"}]}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}

	if request.SystemPrompt != "Be brief." {
		t.Fatalf("unexpected system prompt %q", request.SystemPrompt)
	}
	if len(request.Turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(request.Turns))
	}
	if request.Turns[2].Content != "How are you?" {
		t.Fatalf("unexpected last turn content %q", request.Turns[2].Content)
	}
}

func TestParseOpenAIChatCompletionRequestTrimsSystemPrompt(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"system","content":"  Be brief.  "},
			{"role":"user","content":"Hello"}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.SystemPrompt != "Be brief." {
		t.Fatalf("expected trimmed system prompt, got %q", request.SystemPrompt)
	}
}

func TestParseOpenAIChatCompletionRequestSupportsReasoningAndImage(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"reasoning_effort": "high",
		"x_copilot": {
			"agent": "reviewer",
			"system_message_mode": "replace",
			"include_reasoning": true,
			"include_runtime_events": true
		},
		"messages": [
			{"role":"user","content":[
				{"type":"text","text":"Describe this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}
			]}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning effort to be preserved, got %q", request.ReasoningEffort)
	}
	if request.Copilot.Agent != "reviewer" || request.Copilot.SystemMessageMode != "replace" || !request.Copilot.IncludeReasoning || !request.Copilot.IncludeRuntimeEvents {
		t.Fatalf("expected x_copilot settings to be preserved, got %#v", request.Copilot)
	}
	if len(request.Turns) != 1 || len(request.Turns[0].Attachments) != 1 {
		t.Fatalf("expected one image attachment, got %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestMapsOpenRouterReasoningEffort(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"reasoning": {"effort":"high"},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.ReasoningEffort != "high" {
		t.Fatalf("expected reasoning.effort to map to reasoning effort, got %q", request.ReasoningEffort)
	}
}

func TestParseOpenAIChatCompletionRequestMapsOpenRouterReasoningEnabledToMedium(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"reasoning": {"enabled":true},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning.enabled=true to default to medium effort, got %q", request.ReasoningEffort)
	}
}

func TestParseOpenAIChatCompletionRequestTopLevelReasoningEffortWinsOverEnabled(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"reasoning_effort": "high",
		"reasoning": {"enabled":true},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.ReasoningEffort != "high" {
		t.Fatalf("expected top-level reasoning_effort to win, got %q", request.ReasoningEffort)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsUnsupportedReasoningControls(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "reasoning max tokens",
			body: `{"model":"gpt-4.1","reasoning":{"max_tokens":2000},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "reasoning exclude",
			body: `{"model":"gpt-4.1","reasoning":{"exclude":true},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "conflicting enabled false",
			body: `{"model":"gpt-4.1","reasoning":{"enabled":false,"effort":"high"},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "conflicting top level effort",
			body: `{"model":"gpt-4.1","reasoning_effort":"high","reasoning":{"enabled":false},"messages":[{"role":"user","content":"Hello"}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(tc.body))
			if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
				t.Fatalf("expected unsupported controls error, got %v", err)
			}
		})
	}
}

func TestParseClaudeMessagesRequestMapsTools(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"tools": [{
			"name":"lookup_issue",
			"description":"Look up an issue",
			"input_schema":{
				"type":"object",
				"properties":{"id":{"type":"string"}},
				"required":["id"]
			}
		}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 1 {
		t.Fatalf("expected one mapped Claude tool, got %#v", request.Copilot.Tools)
	}
	if request.Copilot.Tools[0].Name != "lookup_issue" || request.Copilot.Tools[0].Description != "Look up an issue" {
		t.Fatalf("unexpected mapped Claude tool %#v", request.Copilot.Tools[0])
	}
	if request.Copilot.Tools[0].Parameters["type"] != "object" {
		t.Fatalf("expected input_schema to be preserved, got %#v", request.Copilot.Tools[0].Parameters)
	}
}

func TestParseClaudeMessagesRequestPreservesProviderConfig(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"x_copilot": {
			"provider": {
				"type": "anthropic",
				"base_url": "https://api.anthropic.example"
			}
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.Copilot.Provider == nil || request.Copilot.Provider.Type != "anthropic" || request.Copilot.Provider.BaseURL != "https://api.anthropic.example" {
		t.Fatalf("expected provider config to be preserved, got %#v", request.Copilot.Provider)
	}
}

func TestParseClaudeMessagesRequestRejectsServerToolTypes(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"tools": [{"type":"web_search_20250305"}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err == nil {
		t.Fatalf("expected server tools to be rejected")
	}
	if err != ErrUnsupportedTools && !strings.Contains(err.Error(), ErrUnsupportedTools.Error()) {
		t.Fatalf("expected ErrUnsupportedTools, got %v", err)
	}
}

func TestParseClaudeMessagesRequestSupportsReasoningAndImage(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"reasoning_effort": "medium",
		"x_copilot": {
			"include_reasoning": true,
			"include_runtime_events": true
		},
		"messages": [{
			"role":"user",
			"content":[
				{"type":"text","text":"Describe this"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNGBase64 + `"}}
			]
		}]
	}`

	request, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.ReasoningEffort != "medium" {
		t.Fatalf("expected reasoning effort to be preserved, got %q", request.ReasoningEffort)
	}
	if !request.Copilot.IncludeReasoning || !request.Copilot.IncludeRuntimeEvents {
		t.Fatalf("expected x_copilot settings to be preserved, got %#v", request.Copilot)
	}
	if len(request.Turns) != 1 || len(request.Turns[0].Attachments) != 1 {
		t.Fatalf("expected one image attachment, got %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestToleratesStandardControls(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"max_tokens": 128,
		"max_completion_tokens": 256,
		"temperature": 0.1,
		"top_p": 0.9,
		"top_k": 40,
		"presence_penalty": 0.2,
		"frequency_penalty": 0.1,
		"repetition_penalty": 1.05,
		"min_p": 0.05,
		"top_a": 0.1,
		"seed": 42,
		"user": "alice",
		"store": true,
		"metadata": {"source":"sdk"},
		"provider": {"require_parameters": true},
		"route": "fallback",
		"transforms": ["middle-out"],
		"plugins": [{"id":"web"}],
		"verbosity": "high",
		"prediction": {"type":"content","content":"{}"},
		"debug": {"echo_upstream_body": true},
		"response_format": {"type":"json_object"},
		"stream_options": {"include_usage": true},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected standard OpenAI controls to be tolerated, got %v", err)
	}
	if !request.IncludeUsageInStream {
		t.Fatalf("expected stream_options.include_usage to be preserved")
	}
	if len(request.Turns) != 1 || request.Turns[0].Content != "Hello" {
		t.Fatalf("unexpected parsed turns %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsUnsupportedCompatibilityFields(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "parallel tool calls",
			body: `{"model":"gpt-4.1","parallel_tool_calls":true,"tools":[{"type":"function","function":{"name":"lookup_issue"}}],"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "structured outputs",
			body: `{"model":"gpt-4.1","structured_outputs":true,"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "adaptive thinking",
			body: `{"model":"gpt-4.1","adaptive_thinking":{"enabled":true},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "thinking budget",
			body: `{"model":"gpt-4.1","thinking_budget":2048,"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "response format strict schema",
			body: `{"model":"gpt-4.1","response_format":{"type":"json_schema","json_schema":{"name":"summary","strict":true,"schema":{"type":"object"}}},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "response format wrong type",
			body: `{"model":"gpt-4.1","response_format":{"type":123},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "stream options include usage wrong type",
			body: `{"model":"gpt-4.1","stream":true,"stream_options":{"include_usage":"true"},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "tool choice wrong shape",
			body: `{"model":"gpt-4.1","tool_choice":{"function":{"name":123}},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "tool choice missing function name",
			body: `{"model":"gpt-4.1","tool_choice":{"type":"function"},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "tool choice empty function object",
			body: `{"model":"gpt-4.1","tool_choice":{"function":{}},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "function call wrong shape",
			body: `{"model":"gpt-4.1","function_call":{"name":123},"messages":[{"role":"user","content":"Hello"}]}`,
		},
		{
			name: "function call empty name",
			body: `{"model":"gpt-4.1","function_call":{"name":""},"messages":[{"role":"user","content":"Hello"}]}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(tc.body))
			if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
				t.Fatalf("expected unsupported controls error, got %v", err)
			}
		})
	}
}

func TestParseOpenAIChatCompletionRequestAllowsParallelToolCallsFalse(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"parallel_tool_calls": false,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected parallel_tool_calls=false to be tolerated, got %v", err)
	}
	if len(request.Turns) != 1 || request.Turns[0].Content != "Hello" {
		t.Fatalf("unexpected parsed turns %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestAllowsParallelToolCallsWhenToolsDisabled(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"parallel_tool_calls": true,
		"tool_choice": "none",
		"tools": [{"type":"function","function":{"name":"lookup_issue"}}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected tool_choice=none to neutralize parallel_tool_calls, got %v", err)
	}
	if len(request.Copilot.Tools) != 0 {
		t.Fatalf("expected standard tools to be disabled, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsNamedToolChoiceWithoutStandardTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": {"type":"function","function":{"name":"lookup_issue"}},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err == nil {
		t.Fatalf("expected named tool_choice without standard tools to be rejected")
	}
	if err != ErrUnsupportedTools && !strings.Contains(err.Error(), ErrUnsupportedTools.Error()) {
		t.Fatalf("expected unsupported tools error, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestUsesPromptAndModelsFallback(t *testing.T) {
	body := `{
		"models": ["", "gpt-4.1", "gpt-5.4"],
		"prompt": "Hello from prompt"
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.Model != "gpt-4.1" {
		t.Fatalf("expected first non-empty model candidate, got %q", request.Model)
	}
	if len(request.Turns) != 1 || request.Turns[0].Role != "user" || request.Turns[0].Content != "Hello from prompt" {
		t.Fatalf("expected prompt fallback to become one user turn, got %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestConvertsJSONResponseFormat(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"response_format": {
			"type": "json_schema",
			"json_schema": {
				"name": "summary",
				"schema": {
					"type": "object",
					"properties": {
						"title": {"type":"string"}
					},
					"required": ["title"]
				}
			}
		},
		"messages": [{"role":"user","content":"Summarize"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if !strings.Contains(request.SystemPrompt, "Return only valid JSON.") || !strings.Contains(request.SystemPrompt, `"title":{"type":"string"}`) {
		t.Fatalf("expected response_format to inject JSON guidance, got %q", request.SystemPrompt)
	}
}

func TestParseOpenAIChatCompletionRequestMapsStandardTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"stream": true,
		"tools": [{
			"type": "function",
			"function": {
				"name": "lookup_issue",
				"description": "Look up an issue",
				"parameters": {
					"type": "object",
					"properties": {
						"id": {"type":"string"}
					},
					"required": ["id"]
				}
			}
		}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 1 {
		t.Fatalf("expected one mapped tool, got %#v", request.Copilot.Tools)
	}
	tool := request.Copilot.Tools[0]
	if tool.Name != "lookup_issue" || tool.Description != "Look up an issue" {
		t.Fatalf("unexpected mapped tool %#v", tool)
	}
	if tool.Parameters["type"] != "object" {
		t.Fatalf("expected parameters to be preserved, got %#v", tool.Parameters)
	}
}

func TestParseOpenAIChatCompletionRequestPreservesAssistantReasoningFields(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"user","content":"Hello"},
			{
				"role":"assistant",
				"content":"Here is the answer",
				"reasoning_content":"step by step",
				"reasoning_details":[{"type":"reasoning.summary","summary":"kept"}]
			},
			{"role":"user","content":"Continue"}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(request.Turns))
	}
	if !strings.Contains(request.Turns[1].Content, "Reasoning:\nstep by step") {
		t.Fatalf("expected assistant reasoning to be preserved, got %q", request.Turns[1].Content)
	}
	if !strings.Contains(request.Turns[1].Content, `"type":"reasoning.summary"`) {
		t.Fatalf("expected reasoning_details to be preserved, got %q", request.Turns[1].Content)
	}
}

func TestParseOpenAIChatCompletionRequestPreservesProviderConfig(t *testing.T) {
	body := `{
		"model": "custom-model",
		"x_copilot": {
			"provider": {
				"type": "openai",
				"wire_api": "responses",
				"base_url": "https://example.com/v1",
				"api_key": "demo-key"
			}
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.Copilot.Provider == nil {
		t.Fatal("expected provider config to be preserved")
	}
	if request.Copilot.Provider.Type != "openai" || request.Copilot.Provider.WireAPI != "responses" || request.Copilot.Provider.BaseURL != "https://example.com/v1" || request.Copilot.Provider.APIKey != "demo-key" {
		t.Fatalf("unexpected provider config %#v", request.Copilot.Provider)
	}
}

func TestParseOpenAIChatCompletionRequestHonorsToolChoiceNone(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": "none",
		"tools": [{
			"type": "function",
			"function": {"name": "lookup_issue"}
		}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 0 {
		t.Fatalf("expected standard tools to be disabled by tool_choice=none, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestToolChoiceDoesNotDisableNativeCopilotTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": "none",
		"tools": [{
			"type": "function",
			"function": {"name": "lookup_issue"}
		}],
		"x_copilot": {
			"tools": [{
				"name": "native_lookup",
				"description": "Native tool",
				"parameters": {"type":"object"}
			}]
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 1 || request.Copilot.Tools[0].Name != "native_lookup" {
		t.Fatalf("expected tool_choice=none to leave x_copilot.tools untouched, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestToolChoiceFiltersOnlyStandardTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": {"type":"function","function":{"name":"lookup_pr"}},
		"tools": [
			{"type":"function","function":{"name":"lookup_issue"}},
			{"type":"function","function":{"name":"lookup_pr"}}
		],
		"x_copilot": {
			"tools": [{
				"name": "native_lookup",
				"description": "Native tool",
				"parameters": {"type":"object"}
			}]
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 2 || request.Copilot.Tools[0].Name != "lookup_pr" || request.Copilot.Tools[1].Name != "native_lookup" {
		t.Fatalf("expected tool_choice to filter only standard tools, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestToolChoiceIgnoresNativeOnlyTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": {"type":"function","function":{"name":"native_lookup"}},
		"x_copilot": {
			"tools": [{
				"name": "native_lookup",
				"description": "Native tool",
				"parameters": {"type":"object"}
			}]
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected tool_choice to ignore native-only tools, got %v", err)
	}
	if len(request.Copilot.Tools) != 1 || request.Copilot.Tools[0].Name != "native_lookup" {
		t.Fatalf("expected native tool to remain available, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestToolChoiceDoesNotFilterMultipleNativeTools(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": {"type":"function","function":{"name":"native_lookup"}},
		"x_copilot": {
			"tools": [
				{
					"name": "native_lookup",
					"description": "Native tool A",
					"parameters": {"type":"object"}
				},
				{
					"name": "native_other",
					"description": "Native tool B",
					"parameters": {"type":"object"}
				}
			]
		},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected tool_choice to ignore native-only tool filtering, got %v", err)
	}
	if len(request.Copilot.Tools) != 2 || request.Copilot.Tools[0].Name != "native_lookup" || request.Copilot.Tools[1].Name != "native_other" {
		t.Fatalf("expected native tools to remain untouched, got %#v", request.Copilot.Tools)
	}
}

func TestParseOpenAIChatCompletionRequestFiltersSpecificToolChoice(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"tool_choice": {"type":"function","function":{"name":"lookup_pr"}},
		"tools": [
			{"type":"function","function":{"name":"lookup_issue"}},
			{"type":"function","function":{"name":"lookup_pr"}}
		],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if len(request.Copilot.Tools) != 1 || request.Copilot.Tools[0].Name != "lookup_pr" {
		t.Fatalf("expected tool_choice to keep only lookup_pr, got %#v", request.Copilot.Tools)
	}
}

func TestParseClaudeMessagesRequestRejectsControls(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"max_tokens": 128,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls {
		t.Fatalf("expected ErrUnsupportedControls, got %v", err)
	}
}

func TestParseClaudeMessagesRequestRejectsUnknownControls(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"temperature": 0.1,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
		t.Fatalf("expected unsupported controls error, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestSupportsDeveloperAndToolMessages(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"developer","content":"Follow policy"},
			{"role":"user","name":"alice","content":"Need issue details"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup_issue","arguments":"{\"id\":\"123\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","name":"lookup_issue","content":"Issue 123"},
			{"role":"user","content":"Thanks"}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	if request.SystemPrompt != "Follow policy" {
		t.Fatalf("expected developer message to feed system prompt, got %q", request.SystemPrompt)
	}
	if len(request.Turns) != 4 {
		t.Fatalf("expected 4 conversation turns, got %#v", request.Turns)
	}
	if !strings.Contains(request.Turns[0].Content, "Name: alice") {
		t.Fatalf("expected user name to be preserved in prompt content, got %q", request.Turns[0].Content)
	}
	if !strings.Contains(request.Turns[1].Content, "Tool calls:") {
		t.Fatalf("expected assistant tool calls to be preserved, got %q", request.Turns[1].Content)
	}
	if request.Turns[2].Role != "tool" || !strings.Contains(request.Turns[2].Content, "Tool call ID: call_1") || !strings.Contains(request.Turns[2].Content, "Issue 123") {
		t.Fatalf("expected tool message metadata to be preserved, got %#v", request.Turns[2])
	}
}

func TestParseClaudeMessagesRequestRejectsUnsupportedRole(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"messages": [{"role":"tool","content":"Hello"}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != ErrUnsupportedMessageFields && !strings.Contains(err.Error(), ErrUnsupportedMessageFields.Error()) {
		t.Fatalf("expected unsupported message fields error, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestPreservesHistoricalImagesForServerValidation(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}]},
			{"role":"assistant","content":"ok"},
			{"role":"user","content":"next"}
		]
	}`

	request, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("expected parser to preserve attachments for server-side validation, got %v", err)
	}
	if len(request.Turns) != 3 || len(request.Turns[0].Attachments) != 1 {
		t.Fatalf("expected historical attachment to be preserved, got %#v", request.Turns)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsInvalidImagePayload(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}
		]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrInvalidImageData && !strings.Contains(err.Error(), ErrInvalidImageData.Error()) {
		t.Fatalf("expected ErrInvalidImageData, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsSystemImageBlocks(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [
			{"role":"system","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}]},
			{"role":"user","content":"next"}
		]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedImages {
		t.Fatalf("expected ErrUnsupportedImages, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsMultipleChoices(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"n": 2,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
		t.Fatalf("expected unsupported controls error, got %v", err)
	}
}

func TestParseOpenAIChatCompletionRequestRejectsAudioOutput(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"modalities": ["text", "audio"],
		"audio": {"voice":"alloy"},
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
		t.Fatalf("expected unsupported controls error, got %v", err)
	}
}

func TestValidateAttachmentPlacementRejectsNonUserFinalImageTurn(t *testing.T) {
	turns := []Turn{
		{
			Role:    "assistant",
			Content: "here",
			Attachments: []ImageAttachment{{
				MediaType: "image/png",
				Data:      []byte{0x89, 0x50, 0x4e, 0x47},
			}},
		},
	}

	if err := ValidateAttachmentPlacement(turns, true); err != ErrUnsupportedImages {
		t.Fatalf("expected ErrUnsupportedImages, got %v", err)
	}
}

func TestValidateAttachmentPlacementIgnoresNonUserFinalTurnWithoutImages(t *testing.T) {
	turns := []Turn{{Role: "assistant", Content: "done"}}
	if err := ValidateAttachmentPlacement(turns, true); err != nil {
		t.Fatalf("expected non-image assistant tail to be ignored here, got %v", err)
	}
}

func TestParseClaudeMessagesRequestRejectsNonImageMediaType(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"messages": [{
			"role":"user",
			"content":[
				{"type":"image","source":{"type":"base64","media_type":"text/plain","data":"` + tinyPNGBase64 + `"}}
			]
		}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err != ErrInvalidImageData && !strings.Contains(err.Error(), ErrInvalidImageData.Error()) {
		t.Fatalf("expected ErrInvalidImageData, got %v", err)
	}
}
