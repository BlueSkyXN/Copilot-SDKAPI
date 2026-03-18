package compat

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

var (
	ErrNoTurns                  = errors.New("request must include at least one conversation turn")
	ErrFinalTurnNotUser         = errors.New("final conversation turn must be a user message")
	ErrUnsupportedContent       = errors.New("only text and supported image message content are supported")
	ErrUnsupportedTools         = errors.New("tool use is not supported by this gateway yet")
	ErrUnsupportedControls      = errors.New("generation controls are not supported by this gateway yet")
	ErrUnsupportedMessageFields = errors.New("message fields are not supported by this gateway yet")
	ErrUnsupportedImages        = errors.New("image content is only supported on the latest user message")
	ErrInvalidImageData         = errors.New("invalid image content")
)

type Turn struct {
	Role        string
	Content     string
	Attachments []ImageAttachment
}

type CopilotToolDefinition struct {
	Name            string         `json:"name,omitempty"`
	Description     string         `json:"description,omitempty"`
	Parameters      map[string]any `json:"parameters,omitempty"`
	OverrideBuiltIn bool           `json:"override_builtin,omitempty"`
}

type CopilotAttachment struct {
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	Text      string `json:"text,omitempty"`
	URL       string `json:"url,omitempty"`
}

type CopilotRequestExtension struct {
	Agent                string                  `json:"agent,omitempty"`
	SystemMessageMode    string                  `json:"system_message_mode,omitempty"`
	IncludeReasoning     bool                    `json:"include_reasoning,omitempty"`
	IncludeRuntimeEvents bool                    `json:"include_runtime_events,omitempty"`
	Interactive          bool                    `json:"interactive,omitempty"`
	PermissionMode       string                  `json:"permission_mode,omitempty"`
	Tools                []CopilotToolDefinition `json:"tools,omitempty"`
	Attachments          []CopilotAttachment     `json:"attachments,omitempty"`
}

type CopilotResponseExtension struct {
	Agent             string                        `json:"agent,omitempty"`
	SystemMessageMode string                        `json:"system_message_mode,omitempty"`
	Reasoning         string                        `json:"reasoning,omitempty"`
	RuntimeEvents     []gatewayruntime.RuntimeEvent `json:"runtime_events,omitempty"`
}

type ConversationRequest struct {
	Model           string
	SystemPrompt    string
	ReasoningEffort string
	Stream          bool
	Turns           []Turn
	Copilot         CopilotRequestExtension
}

type ImageAttachment struct {
	MediaType string
	Data      []byte
	URL       string
}

func BuildPrompt(turns []Turn) string {
	var builder strings.Builder
	builder.WriteString("Continue the following conversation and reply as the assistant.\n\n")
	for _, turn := range turns {
		builder.WriteString(displayRole(turn.Role))
		builder.WriteString(": ")
		builder.WriteString(turn.Content)
		builder.WriteString("\n\n")
	}
	builder.WriteString("Assistant:")
	return builder.String()
}

func LatestUserPrompt(turns []Turn) (string, error) {
	if len(turns) == 0 {
		return "", ErrNoTurns
	}
	last := turns[len(turns)-1]
	if normalizeRole(last.Role) != "user" {
		return "", ErrFinalTurnNotUser
	}
	return last.Content, nil
}

func LatestUserTurn(turns []Turn) (Turn, error) {
	if len(turns) == 0 {
		return Turn{}, ErrNoTurns
	}
	last := turns[len(turns)-1]
	if normalizeRole(last.Role) != "user" {
		return Turn{}, ErrFinalTurnNotUser
	}
	return last, nil
}

func ValidateAttachmentPlacement(turns []Turn, allowHistorical bool) error {
	if len(turns) == 0 {
		return nil
	}
	if !allowHistorical {
		for _, turn := range turns[:len(turns)-1] {
			if len(turn.Attachments) > 0 {
				return ErrUnsupportedImages
			}
		}
	}
	last := turns[len(turns)-1]
	if normalizeRole(last.Role) != "user" && len(last.Attachments) > 0 {
		return ErrUnsupportedImages
	}
	return nil
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system":
		return "system"
	case "developer":
		return "developer"
	case "assistant":
		return "assistant"
	case "tool":
		return "tool"
	default:
		return "user"
	}
}

func extractTextContent(value any) (string, error) {
	text, attachments, err := extractConversationContent(value)
	if err != nil {
		return "", err
	}
	if len(attachments) > 0 {
		return "", ErrUnsupportedContent
	}
	return text, nil
}

func extractConversationContent(value any) (string, []ImageAttachment, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil, nil
	case string:
		return typed, nil, nil
	case []any:
		var parts []string
		attachments := make([]ImageAttachment, 0)
		for _, item := range typed {
			entry, ok := item.(map[string]any)
			if !ok {
				return "", nil, ErrUnsupportedContent
			}
			itemType, _ := entry["type"].(string)
			switch itemType {
			case "", "text":
				text, ok := entry["text"].(string)
				if !ok {
					return "", nil, ErrUnsupportedContent
				}
				parts = append(parts, text)
			case "image_url":
				attachment, err := parseOpenAIImage(entry)
				if err != nil {
					return "", nil, err
				}
				attachments = append(attachments, attachment)
			case "image":
				attachment, err := parseClaudeImage(entry)
				if err != nil {
					return "", nil, err
				}
				attachments = append(attachments, attachment)
			default:
				return "", nil, fmt.Errorf("%w: %s", ErrUnsupportedContent, itemType)
			}
		}
		return strings.Join(parts, ""), attachments, nil
	default:
		return "", nil, ErrUnsupportedContent
	}
}

func joinPromptParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return strings.Join(filtered, "\n\n")
}

func displayRole(role string) string {
	switch normalizeRole(role) {
	case "assistant":
		return "Assistant"
	case "developer":
		return "Developer"
	case "system":
		return "System"
	case "tool":
		return "Tool"
	default:
		return "User"
	}
}

func parseOpenAIImage(entry map[string]any) (ImageAttachment, error) {
	imageURL, ok := entry["image_url"].(map[string]any)
	if !ok {
		return ImageAttachment{}, ErrInvalidImageData
	}
	rawURL, ok := imageURL["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return ImageAttachment{}, ErrInvalidImageData
	}
	if parsed, err := url.Parse(rawURL); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return ImageAttachment{URL: rawURL}, nil
	}
	return parseDataURLImage(rawURL)
}

func parseClaudeImage(entry map[string]any) (ImageAttachment, error) {
	source, ok := entry["source"].(map[string]any)
	if !ok {
		return ImageAttachment{}, ErrInvalidImageData
	}
	sourceType, _ := source["type"].(string)
	if sourceType == "url" {
		rawURL, _ := source["url"].(string)
		if strings.TrimSpace(rawURL) == "" {
			return ImageAttachment{}, ErrInvalidImageData
		}
		if parsed, err := url.Parse(rawURL); err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
			return ImageAttachment{URL: rawURL}, nil
		}
		return ImageAttachment{}, fmt.Errorf("%w: source type %s", ErrInvalidImageData, sourceType)
	}
	if sourceType != "base64" {
		return ImageAttachment{}, fmt.Errorf("%w: source type %s", ErrInvalidImageData, sourceType)
	}
	mediaType, _ := source["media_type"].(string)
	data, _ := source["data"].(string)
	if mediaType == "" || data == "" {
		return ImageAttachment{}, ErrInvalidImageData
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("%w: %v", ErrInvalidImageData, err)
	}
	if err := validateImagePayload(mediaType, decoded); err != nil {
		return ImageAttachment{}, err
	}
	return ImageAttachment{MediaType: normalizeImageMediaType(mediaType), Data: decoded}, nil
}

func parseDataURLImage(raw string) (ImageAttachment, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("%w: %v", ErrInvalidImageData, err)
	}
	if parsed.Scheme != "data" {
		return ImageAttachment{}, fmt.Errorf("%w: only data URLs are supported", ErrInvalidImageData)
	}

	content := strings.TrimPrefix(raw, "data:")
	header, encoded, found := strings.Cut(content, ",")
	if !found {
		return ImageAttachment{}, ErrInvalidImageData
	}
	mediaType, encoding, found := strings.Cut(header, ";")
	if !found || encoding != "base64" || mediaType == "" {
		return ImageAttachment{}, ErrInvalidImageData
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ImageAttachment{}, fmt.Errorf("%w: %v", ErrInvalidImageData, err)
	}
	if err := validateImagePayload(mediaType, decoded); err != nil {
		return ImageAttachment{}, err
	}
	return ImageAttachment{MediaType: normalizeImageMediaType(mediaType), Data: decoded}, nil
}

func validateImagePayload(mediaType string, data []byte) error {
	mediaType = normalizeImageMediaType(mediaType)
	if !strings.HasPrefix(mediaType, "image/") {
		return fmt.Errorf("%w: media type %s", ErrInvalidImageData, mediaType)
	}
	switch mediaType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return fmt.Errorf("%w: media type %s", ErrInvalidImageData, mediaType)
	}
	if len(data) == 0 {
		return ErrInvalidImageData
	}

	detected := strings.ToLower(strings.TrimSpace(http.DetectContentType(data)))
	switch mediaType {
	case "image/webp":
		if detected != "image/webp" {
			return fmt.Errorf("%w: declared %s but detected %s", ErrInvalidImageData, mediaType, detected)
		}
		return nil
	case "image/png", "image/jpeg", "image/gif":
		_, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidImageData, err)
		}
		expected := map[string]string{
			"image/png":  "png",
			"image/jpeg": "jpeg",
			"image/gif":  "gif",
		}[mediaType]
		if format != expected {
			return fmt.Errorf("%w: declared %s but decoded %s", ErrInvalidImageData, mediaType, format)
		}
		return nil
	default:
		return ErrInvalidImageData
	}
}

func normalizeImageMediaType(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "image/jpg" {
		return "image/jpeg"
	}
	return mediaType
}
