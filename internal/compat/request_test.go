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

func TestParseClaudeMessagesRequestRejectsTools(t *testing.T) {
	body := `{
		"model": "claude-sonnet-4.5",
		"tools": [{"name":"x"}],
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseClaudeMessagesRequest(strings.NewReader(body))
	if err == nil {
		t.Fatalf("expected tools to be rejected")
	}
	if err != ErrUnsupportedTools {
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

func TestParseOpenAIChatCompletionRequestRejectsControls(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"temperature": 0.1,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls {
		t.Fatalf("expected ErrUnsupportedControls, got %v", err)
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

func TestParseOpenAIChatCompletionRequestRejectsUnknownControls(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"presence_penalty": 0.1,
		"messages": [{"role":"user","content":"Hello"}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedControls && !strings.Contains(err.Error(), ErrUnsupportedControls.Error()) {
		t.Fatalf("expected unsupported controls error, got %v", err)
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

func TestParseOpenAIChatCompletionRequestRejectsUnsupportedMessageFields(t *testing.T) {
	body := `{
		"model": "gpt-4.1",
		"messages": [{"role":"assistant","content":"Hello","tool_calls":[]}]
	}`

	_, err := ParseOpenAIChatCompletionRequest(strings.NewReader(body))
	if err != ErrUnsupportedMessageFields && !strings.Contains(err.Error(), ErrUnsupportedMessageFields.Error()) {
		t.Fatalf("expected unsupported message fields error, got %v", err)
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
