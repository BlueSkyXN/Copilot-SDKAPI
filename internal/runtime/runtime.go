package runtime

import "context"

type PermissionMode string

const (
	PermissionModeDeny  PermissionMode = "deny"
	PermissionModeAllow PermissionMode = "allow"
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
}

type SessionOptions struct {
	Model            string
	SystemPrompt     string
	SystemPromptMode string
	ReasoningEffort  string
	Agent            string
}

type Attachment struct {
	Path      string
	MediaType string
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
	EventUserInputRequested        EventType = "user_input_requested"
	EventSessionCompactionStart    EventType = "session_compaction_start"
	EventSessionCompactionComplete EventType = "session_compaction_complete"
	EventSystemMessage             EventType = "system_message"
	EventSkillInvoked              EventType = "skill_invoked"
	EventSubagentSelected          EventType = "subagent_selected"
	EventUsage                     EventType = "usage"
	EventError                     EventType = "error"
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
	Close() error
}

type Provider interface {
	Start(ctx context.Context) error
	Close() error
	ListModels(ctx context.Context) ([]Model, error)
	NewSession(ctx context.Context, options SessionOptions) (Session, error)
}
