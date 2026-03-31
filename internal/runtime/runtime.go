package runtime

import (
	"context"
	"errors"
)

var ErrSessionNotFound = errors.New("runtime session not found")
var ErrPendingRequestNotFound = errors.New("pending request not found")
var ErrPendingRequestsUnsupported = errors.New("pending request continuation is not supported")

type PermissionMode string

const (
	PermissionModeDeny    PermissionMode = "deny"
	PermissionModeAllow   PermissionMode = "allow"
	PermissionModeBridge  PermissionMode = "bridge"
	PermissionModeInherit PermissionMode = "inherit"
)

type InfiniteSessionOptions struct {
	Enabled                       *bool
	BackgroundCompactionThreshold *float64
	BufferExhaustionThreshold     *float64
}

type CustomAgent struct {
	Name        string
	DisplayName string
	Description string
	Tools       []string
	Prompt      string
	MCPServers  map[string]map[string]any
	Infer       *bool
}

type ProviderConfig struct {
	Type            string
	WireAPI         string
	BaseURL         string
	APIKey          string
	BearerToken     string
	AzureAPIVersion string
}

type TelemetryOptions struct {
	Enabled        bool
	Endpoint       string
	FilePath       string
	ExporterType   string
	SourceName     string
	CaptureContent *bool
}

type Options struct {
	CLIPath          string
	GitHubToken      string
	UseLoggedInUser  bool
	LogLevel         string
	ClientName       string
	WorkingDirectory string
	ConfigDir        string
	DefaultModel     string
	AvailableTools   []string
	ExcludedTools    []string
	SkillDirectories []string
	DisabledSkills   []string
	MCPServers       map[string]map[string]any
	CustomAgents     []CustomAgent
	DefaultAgent     string
	PermissionMode   PermissionMode
	InfiniteSessions *InfiniteSessionOptions
	Telemetry        *TelemetryOptions
}

type SectionOverride struct {
	Action  string `json:"action,omitempty"`
	Content string `json:"content,omitempty"`
}

type SessionOptions struct {
	SessionID             string
	Model                 string
	SystemPrompt          string
	SystemPromptMode      string
	SystemMessageSections map[string]SectionOverride
	ReasoningEffort       string
	Agent                 string
	Interactive           bool
	Tools                 []ToolDefinition
	PermissionMode        PermissionMode
	Provider              *ProviderConfig
}

type Attachment struct {
	Path      string
	MediaType string
	// BlobData holds base64-encoded inline data for blob attachments.
	// When set, the attachment is sent as a blob instead of a file reference.
	BlobData string
}

type ToolDefinition struct {
	Name            string
	Description     string
	Parameters      map[string]any
	OverrideBuiltIn bool
	SkipPermission  bool
}

type ToolBinaryResult struct {
	Data        string `json:"data"`
	MIMEType    string `json:"mime_type,omitempty"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

type ToolResult struct {
	TextResult    string
	BinaryResults []ToolBinaryResult
	ResultType    string
	SessionLog    string
	Telemetry     map[string]any
}

type UserInputResponse struct {
	Answer      string
	WasFreeform bool
}

type PermissionResponse struct {
	ResultKind string
	Rules      []any
}

type PendingRequestKind string

const (
	PendingRequestTool       PendingRequestKind = "tool"
	PendingRequestUserInput  PendingRequestKind = "user_input"
	PendingRequestPermission PendingRequestKind = "permission"
)

type PendingRequest struct {
	ID                string
	Kind              PendingRequestKind
	CallID            string
	ToolName          string
	Arguments         any
	PermissionRequest *PermissionRequestSummary
	UserInputRequest  *UserInputRequestSummary
}

type PendingResponse struct {
	RequestID  string
	Kind       PendingRequestKind
	ToolResult *ToolResult
	ToolError  string
	UserInput  *UserInputResponse
	Permission *PermissionResponse
}

type MessageOptions struct {
	Prompt      string
	Attachments []Attachment
}

type EventType string

const (
	EventMessageDelta              EventType = "message_delta"
	EventMessage                   EventType = "message"
	EventReasoningDelta            EventType = "reasoning_delta"
	EventReasoning                 EventType = "reasoning"
	EventToolExecutionStart        EventType = "tool_execution_start"
	EventToolExecutionComplete     EventType = "tool_execution_complete"
	EventPermissionRequested       EventType = "permission_requested"
	EventPermissionCompleted       EventType = "permission_completed"
	EventUserInputRequested        EventType = "user_input_requested"
	EventExternalToolRequested     EventType = "external_tool_requested"
	EventSessionCompactionStart    EventType = "session_compaction_start"
	EventSessionCompactionComplete EventType = "session_compaction_complete"
	EventSystemMessage             EventType = "system_message"
	EventSkillInvoked              EventType = "skill_invoked"
	EventSubagentSelected          EventType = "subagent_selected"
	EventSubagentStarted           EventType = "subagent_started"
	EventSubagentCompleted         EventType = "subagent_completed"
	EventSubagentFailed            EventType = "subagent_failed"
	EventAssistantIntent           EventType = "assistant_intent"
	EventAssistantTurnStart        EventType = "assistant_turn_start"
	EventAssistantTurnEnd          EventType = "assistant_turn_end"
	EventSessionWarning            EventType = "session_warning"
	EventSessionModelChange        EventType = "session_model_change"
	EventToolExecutionProgress     EventType = "tool_execution_progress"
	EventToolExecutionPartialResult EventType = "tool_execution_partial_result"
	EventUsage                     EventType = "usage"
	EventError                     EventType = "error"
	EventGeneric                   EventType = "generic"
)

type PermissionRequestSummary struct {
	Kind       string   `json:"kind,omitempty"`
	ToolName   string   `json:"tool_name,omitempty"`
	ServerName string   `json:"server_name,omitempty"`
	Path       string   `json:"path,omitempty"`
	URL        string   `json:"url,omitempty"`
	Intention  string   `json:"intention,omitempty"`
	Warning    string   `json:"warning,omitempty"`
	ReadOnly   *bool    `json:"read_only,omitempty"`
	Commands   []string `json:"commands,omitempty"`
}

type UserInputRequestSummary struct {
	Question        string         `json:"question,omitempty"`
	Choices         []string       `json:"choices,omitempty"`
	AllowFreeform   *bool          `json:"allow_freeform,omitempty"`
	RequestedSchema map[string]any `json:"requested_schema,omitempty"`
}

type RuntimeEvent struct {
	Type              string                    `json:"type"`
	Content           string                    `json:"content,omitempty"`
	Delta             string                    `json:"delta,omitempty"`
	Role              string                    `json:"role,omitempty"`
	Name              string                    `json:"name,omitempty"`
	RequestID         string                    `json:"request_id,omitempty"`
	CallID            string                    `json:"call_id,omitempty"`
	ToolName          string                    `json:"tool_name,omitempty"`
	MCPServerName     string                    `json:"mcp_server_name,omitempty"`
	MCPToolName       string                    `json:"mcp_tool_name,omitempty"`
	Arguments         any                       `json:"arguments,omitempty"`
	Success           *bool                     `json:"success,omitempty"`
	Result            string                    `json:"result,omitempty"`
	DetailedResult    string                    `json:"detailed_result,omitempty"`
	Telemetry         map[string]any            `json:"telemetry,omitempty"`
	PermissionRequest *PermissionRequestSummary `json:"permission_request,omitempty"`
	UserInputRequest  *UserInputRequestSummary  `json:"user_input_request,omitempty"`
	SummaryContent    string                    `json:"summary_content,omitempty"`
	TokensRemoved     int64                     `json:"tokens_removed,omitempty"`
	AgentName         string                    `json:"agent_name,omitempty"`
	AgentDisplayName  string                    `json:"agent_display_name,omitempty"`
	AgentDescription  string                    `json:"agent_description,omitempty"`
	AllowedTools      []string                  `json:"allowed_tools,omitempty"`
	TurnID            string                    `json:"turn_id,omitempty"`
	Model             string                    `json:"model,omitempty"`
	PreviousModel     string                    `json:"previous_model,omitempty"`
	DurationMS        float64                   `json:"duration_ms,omitempty"`
	TotalTokens       int64                     `json:"total_tokens,omitempty"`
	TotalToolCalls    int64                     `json:"total_tool_calls,omitempty"`
	// Data holds raw event payload for generic passthrough events.
	Data              map[string]any            `json:"data,omitempty"`
}

type Usage struct {
	InputTokens      int64 `json:"input_tokens,omitempty"`
	OutputTokens     int64 `json:"output_tokens,omitempty"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
}

func (u Usage) TotalTokens() int64 {
	return u.InputTokens + u.OutputTokens
}

type ResponseError struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	StatusCode int    `json:"status_code,omitempty"`
}

func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Type != "" {
		return e.Type
	}
	return "runtime error"
}

type Event struct {
	Type    EventType
	Delta   string
	Content string
	Usage   Usage
	Err     *ResponseError
	Runtime *RuntimeEvent
}

type Result struct {
	SessionID     string
	Content       string
	Reasoning     string
	Usage         Usage
	RuntimeEvents []RuntimeEvent
}

type EventHandler func(Event) error

type Model struct {
	ID                        string        `json:"id"`
	Name                      string        `json:"name"`
	Supports                  ModelSupports `json:"supports,omitempty"`
	Limits                    ModelLimits   `json:"limits,omitempty"`
	SupportedReasoningEfforts []string      `json:"supported_reasoning_efforts,omitempty"`
	DefaultReasoningEffort    string        `json:"default_reasoning_effort,omitempty"`
}

type ModelSupports struct {
	Vision          bool `json:"vision,omitempty"`
	ReasoningEffort bool `json:"reasoning_effort,omitempty"`
}

type ModelVisionLimits struct {
	SupportedMediaTypes []string `json:"supported_media_types,omitempty"`
	MaxPromptImages     int      `json:"max_prompt_images,omitempty"`
	MaxPromptImageSize  int      `json:"max_prompt_image_size,omitempty"`
}

type ModelLimits struct {
	MaxPromptTokens        *int               `json:"max_prompt_tokens,omitempty"`
	MaxOutputTokens        *int               `json:"max_output_tokens,omitempty"`
	MaxContextWindowTokens int                `json:"max_context_window_tokens,omitempty"`
	Vision                 *ModelVisionLimits `json:"vision,omitempty"`
}

type Session interface {
	ID() string
	Send(ctx context.Context, message MessageOptions, handler EventHandler) (Result, error)
	ResolvePending(ctx context.Context, response PendingResponse) error
	Close() error
}

type Provider interface {
	Start(ctx context.Context) error
	Close() error
	ListModels(ctx context.Context) ([]Model, error)
	NewSession(ctx context.Context, options SessionOptions) (Session, error)
	ResumeSession(ctx context.Context, sessionID string, options SessionOptions) (Session, error)
	DeleteSession(ctx context.Context, sessionID string) error
}
