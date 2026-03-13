package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
)

type copilotProvider struct {
	options Options
	mu      sync.Mutex
	client  *copilot.Client
}

type copilotSession struct {
	session *copilot.Session
}

func NewCopilotProvider(options Options) Provider {
	return &copilotProvider{options: options}
}

func (p *copilotProvider) Start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return nil
	}

	client := copilot.NewClient(&copilot.ClientOptions{
		CLIPath:         p.options.CLIPath,
		LogLevel:        p.options.LogLevel,
		Cwd:             p.options.WorkingDirectory,
		GitHubToken:     p.options.GitHubToken,
		UseLoggedInUser: copilot.Bool(p.options.UseLoggedInUser),
	})
	if err := client.Start(ctx); err != nil {
		return fmt.Errorf("start copilot client: %w", err)
	}
	p.client = client
	return nil
}

func (p *copilotProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client == nil {
		return nil
	}
	err := p.client.Stop()
	p.client = nil
	return err
}

func (p *copilotProvider) ListModels(ctx context.Context) ([]Model, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	if client == nil {
		return nil, errors.New("copilot client is not started")
	}

	models, err := client.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}

	result := make([]Model, 0, len(models))
	for _, model := range models {
		name := model.Name
		if name == "" {
			name = model.ID
		}
		var maxPromptTokens *int
		if model.Capabilities.Limits.MaxPromptTokens != nil {
			value := *model.Capabilities.Limits.MaxPromptTokens
			maxPromptTokens = &value
		}
		var visionLimits *ModelVisionLimits
		if model.Capabilities.Limits.Vision != nil {
			visionLimits = &ModelVisionLimits{
				SupportedMediaTypes: append([]string(nil), model.Capabilities.Limits.Vision.SupportedMediaTypes...),
				MaxPromptImages:     model.Capabilities.Limits.Vision.MaxPromptImages,
				MaxPromptImageSize:  model.Capabilities.Limits.Vision.MaxPromptImageSize,
			}
		}
		result = append(result, Model{
			ID:   model.ID,
			Name: name,
			Supports: ModelSupports{
				Vision:          model.Capabilities.Supports.Vision,
				ReasoningEffort: model.Capabilities.Supports.ReasoningEffort,
			},
			Limits: ModelLimits{
				MaxPromptTokens:        maxPromptTokens,
				MaxContextWindowTokens: model.Capabilities.Limits.MaxContextWindowTokens,
				Vision:                 visionLimits,
			},
			SupportedReasoningEfforts: append([]string(nil), model.SupportedReasoningEfforts...),
			DefaultReasoningEffort:    model.DefaultReasoningEffort,
		})
	}
	return result, nil
}

func (p *copilotProvider) NewSession(ctx context.Context, options SessionOptions) (Session, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()

	if client == nil {
		return nil, errors.New("copilot client is not started")
	}

	config := &copilot.SessionConfig{
		ClientName:          p.options.ClientName,
		Model:               firstNonEmpty(options.Model, p.options.DefaultModel),
		ConfigDir:           p.options.ConfigDir,
		WorkingDirectory:    p.options.WorkingDirectory,
		Streaming:           true,
		OnPermissionRequest: permissionHandlerForMode(p.options.PermissionMode),
		AvailableTools:      append([]string(nil), p.options.AvailableTools...),
		ExcludedTools:       append([]string(nil), p.options.ExcludedTools...),
		SkillDirectories:    append([]string(nil), p.options.SkillDirectories...),
		DisabledSkills:      append([]string(nil), p.options.DisabledSkills...),
	}
	if options.ReasoningEffort != "" {
		config.ReasoningEffort = options.ReasoningEffort
	}
	if len(p.options.MCPServers) > 0 {
		config.MCPServers = copyMCPServers(p.options.MCPServers)
	}
	if len(p.options.CustomAgents) > 0 {
		config.CustomAgents = copyCustomAgents(p.options.CustomAgents)
	}
	agentName := firstNonEmpty(options.Agent, p.options.DefaultAgent)
	if p.options.InfiniteSessions != nil {
		config.InfiniteSessions = &copilot.InfiniteSessionConfig{
			Enabled:                       p.options.InfiniteSessions.Enabled,
			BackgroundCompactionThreshold: p.options.InfiniteSessions.BackgroundCompactionThreshold,
			BufferExhaustionThreshold:     p.options.InfiniteSessions.BufferExhaustionThreshold,
		}
	}
	if options.SystemPrompt != "" {
		config.SystemMessage = &copilot.SystemMessageConfig{
			Mode:    normalizeSystemMessageMode(options.SystemPromptMode),
			Content: options.SystemPrompt,
		}
	}

	session, err := client.CreateSession(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create copilot session: %w", err)
	}
	if agentName != "" {
		if _, err := session.RPC.Agent.Select(ctx, &rpc.SessionAgentSelectParams{Name: agentName}); err != nil {
			_ = session.Disconnect()
			return nil, fmt.Errorf("select agent %q: %w", agentName, err)
		}
	}
	return &copilotSession{session: session}, nil
}

func (s *copilotSession) ID() string {
	return s.session.SessionID
}

func (s *copilotSession) Close() error {
	return s.session.Disconnect()
}

func (s *copilotSession) Send(ctx context.Context, message MessageOptions, handler EventHandler) (Result, error) {
	sendCtx, cancelSend := context.WithCancelCause(ctx)
	defer cancelSend(nil)

	var (
		stateMu        sync.Mutex
		contentBuf     strings.Builder
		reasoningBuf   strings.Builder
		finalText      string
		finalReasoning string
		usage          Usage
		runtimeEvents  []RuntimeEvent
		firstErr       error
	)

	setError := func(err error) {
		stateMu.Lock()
		defer stateMu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	dispatch := func(ev Event) {
		if ev.Runtime != nil {
			stateMu.Lock()
			runtimeEvents = append(runtimeEvents, *ev.Runtime)
			if ev.Type == EventReasoning && ev.Runtime.Content != "" {
				finalReasoning = ev.Runtime.Content
			}
			stateMu.Unlock()
		}
		if handler != nil {
			if err := handler(ev); err != nil {
				setError(err)
				cancelSend(err)
			}
		}
	}

	idleCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)

	unsubscribe := s.session.On(func(event copilot.SessionEvent) {
		switch event.Type {
		case copilot.AssistantMessageDelta:
			delta := deref(event.Data.DeltaContent)
			if delta == "" {
				return
			}
			stateMu.Lock()
			contentBuf.WriteString(delta)
			stateMu.Unlock()
			dispatch(Event{Type: EventMessageDelta, Delta: delta})
		case copilot.AssistantMessage:
			content := deref(event.Data.Content)
			if content == "" {
				return
			}
			stateMu.Lock()
			finalText = content
			stateMu.Unlock()
			dispatch(Event{Type: EventMessage, Content: content})
		case copilot.AssistantReasoningDelta:
			delta := firstNonEmpty(deref(event.Data.DeltaContent), deref(event.Data.ReasoningText))
			if delta == "" {
				return
			}
			stateMu.Lock()
			reasoningBuf.WriteString(delta)
			stateMu.Unlock()
			dispatch(Event{Type: EventReasoningDelta, Runtime: &RuntimeEvent{Type: string(EventReasoningDelta), Delta: delta}})
		case copilot.AssistantReasoning:
			content := firstNonEmpty(deref(event.Data.Content), deref(event.Data.ReasoningText))
			if content == "" {
				stateMu.Lock()
				content = reasoningBuf.String()
				stateMu.Unlock()
			}
			if content == "" {
				return
			}
			dispatch(Event{Type: EventReasoning, Runtime: &RuntimeEvent{Type: string(EventReasoning), Content: content}})
		case copilot.ToolExecutionStart:
			dispatch(Event{Type: EventToolExecutionStart, Runtime: &RuntimeEvent{
				Type:          string(EventToolExecutionStart),
				CallID:        deref(event.Data.ToolCallID),
				ToolName:      deref(event.Data.ToolName),
				MCPServerName: deref(event.Data.MCPServerName),
				MCPToolName:   deref(event.Data.MCPToolName),
				Arguments:     event.Data.Arguments,
			}})
		case copilot.ToolExecutionComplete:
			dispatch(Event{Type: EventToolExecutionComplete, Runtime: &RuntimeEvent{
				Type:           string(EventToolExecutionComplete),
				CallID:         deref(event.Data.ToolCallID),
				ToolName:       deref(event.Data.ToolName),
				MCPServerName:  deref(event.Data.MCPServerName),
				MCPToolName:    deref(event.Data.MCPToolName),
				Arguments:      event.Data.Arguments,
				Success:        boolPtr(event.Data.Success),
				Result:         resultContent(event.Data.Result),
				DetailedResult: resultDetailedContent(event.Data.Result),
				Telemetry:      cloneMap(event.Data.ToolTelemetry),
				Content:        errorUnionMessage(event.Data.Error),
			}})
		case copilot.PermissionRequested:
			dispatch(Event{Type: EventPermissionRequested, Runtime: &RuntimeEvent{
				Type:              string(EventPermissionRequested),
				CallID:            deref(event.Data.ToolCallID),
				PermissionRequest: permissionSummary(event.Data.PermissionRequest),
			}})
		case copilot.UserInputRequested:
			dispatch(Event{Type: EventUserInputRequested, Runtime: &RuntimeEvent{
				Type:             string(EventUserInputRequested),
				UserInputRequest: userInputSummary(event.Data),
			}})
		case copilot.SessionCompactionStart:
			dispatch(Event{Type: EventSessionCompactionStart, Runtime: &RuntimeEvent{Type: string(EventSessionCompactionStart)}})
		case copilot.SessionCompactionComplete:
			dispatch(Event{Type: EventSessionCompactionComplete, Runtime: &RuntimeEvent{
				Type:           string(EventSessionCompactionComplete),
				SummaryContent: deref(event.Data.SummaryContent),
				TokensRemoved:  int64FromFloat(event.Data.TokensRemoved),
				Success:        boolPtr(event.Data.Success),
				Content:        errorUnionMessage(event.Data.Error),
			}})
		case copilot.SystemMessage:
			dispatch(Event{Type: EventSystemMessage, Runtime: &RuntimeEvent{
				Type:    string(EventSystemMessage),
				Role:    enumString(event.Data.Role),
				Content: deref(event.Data.Content),
				Name:    deref(event.Data.Name),
			}})
		case copilot.SkillInvoked:
			dispatch(Event{Type: EventSkillInvoked, Runtime: &RuntimeEvent{
				Type:         string(EventSkillInvoked),
				Name:         deref(event.Data.Name),
				AllowedTools: append([]string(nil), event.Data.AllowedTools...),
			}})
		case copilot.SubagentSelected:
			dispatch(Event{Type: EventSubagentSelected, Runtime: &RuntimeEvent{
				Type:             string(EventSubagentSelected),
				AgentName:        deref(event.Data.AgentName),
				AgentDisplayName: deref(event.Data.AgentDisplayName),
				AgentDescription: deref(event.Data.AgentDescription),
				AllowedTools:     append([]string(nil), event.Data.Tools...),
			}})
		case copilot.AssistantUsage:
			currentUsage := Usage{
				InputTokens:      int64FromFloat(event.Data.InputTokens),
				OutputTokens:     int64FromFloat(event.Data.OutputTokens),
				CacheReadTokens:  int64FromFloat(event.Data.CacheReadTokens),
				CacheWriteTokens: int64FromFloat(event.Data.CacheWriteTokens),
			}
			stateMu.Lock()
			usage = currentUsage
			stateMu.Unlock()
			dispatch(Event{Type: EventUsage, Usage: currentUsage})
		case copilot.SessionError:
			runtimeErr := runtimeErrorFromEvent(event)
			dispatch(Event{Type: EventError, Err: runtimeErr})
			cancelSend(runtimeErr)
			select {
			case errCh <- runtimeErr:
			default:
			}
		case copilot.SessionIdle:
			select {
			case idleCh <- struct{}{}:
			default:
			}
		}

		if cause := context.Cause(sendCtx); cause != nil {
			select {
			case errCh <- cause:
			default:
			}
		}
	})
	defer unsubscribe()

	type sendResult struct {
		err error
	}
	sendCh := make(chan sendResult, 1)
	go func() {
		request := copilot.MessageOptions{Prompt: message.Prompt}
		if len(message.Attachments) > 0 {
			request.Attachments = make([]copilot.Attachment, 0, len(message.Attachments))
			for _, attachment := range message.Attachments {
				path := attachment.Path
				request.Attachments = append(request.Attachments, copilot.Attachment{
					Type: copilot.File,
					Path: &path,
				})
			}
		}
		_, err := s.session.Send(sendCtx, request)
		sendCh <- sendResult{err: err}
	}()

	select {
	case send := <-sendCh:
		if send.err != nil {
			return Result{}, fmt.Errorf("send prompt: %w", send.err)
		}
	case <-sendCtx.Done():
		cause := context.Cause(sendCtx)
		if cause == nil {
			cause = sendCtx.Err()
		}
		return Result{}, fmt.Errorf("send prompt: %w", cause)
	}

	select {
	case err := <-errCh:
		return Result{}, err
	case <-idleCh:
	case <-sendCtx.Done():
		cause := context.Cause(sendCtx)
		if cause == nil {
			cause = sendCtx.Err()
		}
		return Result{}, fmt.Errorf("wait for response: %w", cause)
	}

	stateMu.Lock()
	defer stateMu.Unlock()

	if firstErr != nil {
		return Result{}, firstErr
	}
	if finalText == "" {
		finalText = contentBuf.String()
	}
	if finalReasoning == "" {
		finalReasoning = reasoningBuf.String()
	}
	return Result{
		SessionID:     s.session.SessionID,
		Content:       finalText,
		Reasoning:     finalReasoning,
		Usage:         usage,
		RuntimeEvents: append([]RuntimeEvent(nil), runtimeEvents...),
	}, nil
}

func permissionHandlerForMode(mode PermissionMode) copilot.PermissionHandlerFunc {
	switch mode {
	case PermissionModeAllow:
		return allowAllPermissions
	default:
		return denyAllPermissions
	}
}

func allowAllPermissions(_ copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	return copilot.PermissionRequestResult{
		Kind: copilot.PermissionRequestResultKindApproved,
	}, nil
}

func denyAllPermissions(_ copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	return copilot.PermissionRequestResult{
		Kind: copilot.PermissionRequestResultKindDeniedByRules,
	}, nil
}

func copyMCPServers(servers map[string]map[string]any) map[string]copilot.MCPServerConfig {
	copiedServers := make(map[string]copilot.MCPServerConfig, len(servers))
	for name, server := range servers {
		copied := make(copilot.MCPServerConfig, len(server))
		for key, value := range server {
			copied[key] = value
		}
		copiedServers[name] = copied
	}
	return copiedServers
}

func copyCustomAgents(agents []CustomAgent) []copilot.CustomAgentConfig {
	copied := make([]copilot.CustomAgentConfig, 0, len(agents))
	for _, agent := range agents {
		item := copilot.CustomAgentConfig{
			Name:        agent.Name,
			DisplayName: agent.DisplayName,
			Description: agent.Description,
			Tools:       append([]string(nil), agent.Tools...),
			Prompt:      agent.Prompt,
			Infer:       agent.Infer,
		}
		if len(agent.MCPServers) > 0 {
			item.MCPServers = copyMCPServers(agent.MCPServers)
		}
		copied = append(copied, item)
	}
	return copied
}

func normalizeSystemMessageMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "replace":
		return "replace"
	default:
		return "append"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func enumString[T ~string](value *T) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	copied := make(map[string]any, len(input))
	for key, value := range input {
		copied[key] = value
	}
	return copied
}

func permissionSummary(request *copilot.PermissionRequest) *PermissionRequestSummary {
	if request == nil {
		return nil
	}
	summary := &PermissionRequestSummary{
		Kind:       string(request.Kind),
		ToolName:   deref(request.ToolName),
		ServerName: deref(request.ServerName),
		Path:       deref(request.Path),
		URL:        deref(request.URL),
		Intention:  deref(request.Intention),
		Warning:    deref(request.Warning),
		ReadOnly:   boolPtr(request.ReadOnly),
	}
	if len(request.Commands) > 0 {
		summary.Commands = make([]string, 0, len(request.Commands))
		for _, command := range request.Commands {
			summary.Commands = append(summary.Commands, command.Identifier)
		}
	}
	return summary
}

func userInputSummary(data copilot.Data) *UserInputRequestSummary {
	if data.Question == nil && len(data.Choices) == 0 && data.RequestedSchema == nil {
		return nil
	}
	summary := &UserInputRequestSummary{
		Question:      deref(data.Question),
		Choices:       append([]string(nil), data.Choices...),
		AllowFreeform: boolPtr(data.AllowFreeform),
	}
	if data.RequestedSchema != nil {
		summary.RequestedSchema = map[string]any{
			"type":       string(data.RequestedSchema.Type),
			"properties": cloneMap(data.RequestedSchema.Properties),
		}
		if len(data.RequestedSchema.Required) > 0 {
			summary.RequestedSchema["required"] = append([]string(nil), data.RequestedSchema.Required...)
		}
	}
	return summary
}

func resultContent(result *copilot.Result) string {
	if result == nil {
		return ""
	}
	return deref(result.Content)
}

func resultDetailedContent(result *copilot.Result) string {
	if result == nil {
		return ""
	}
	return deref(result.DetailedContent)
}

func errorUnionMessage(value *copilot.ErrorUnion) string {
	if value == nil {
		return ""
	}
	if value.String != nil {
		return *value.String
	}
	if value.ErrorClass != nil {
		return value.ErrorClass.Message
	}
	return ""
}

func int64FromFloat(value *float64) int64 {
	if value == nil {
		return 0
	}
	return int64(math.Round(*value))
}

func intFromInt64(value *int64) int {
	if value == nil {
		return 0
	}
	return int(*value)
}

func runtimeErrorFromEvent(event copilot.SessionEvent) *ResponseError {
	errorType := strings.TrimSpace(deref(event.Data.ErrorType))
	message := strings.TrimSpace(deref(event.Data.Message))
	if message == "" {
		message = "copilot session error"
	}
	return &ResponseError{
		Type:       errorType,
		Message:    message,
		StatusCode: firstNonZeroStatus(intFromInt64(event.Data.StatusCode), statusCodeForErrorType(errorType)),
	}
}

func statusCodeForErrorType(errorType string) int {
	switch strings.ToLower(strings.TrimSpace(errorType)) {
	case "authentication":
		return 401
	case "authorization":
		return 403
	case "rate_limit", "quota":
		return 429
	case "query", "invalid_request":
		return 400
	default:
		return 502
	}
}

func firstNonZeroStatus(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
