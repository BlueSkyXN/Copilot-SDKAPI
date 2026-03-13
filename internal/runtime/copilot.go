package runtime

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

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
	bridge  *interactiveBridge
}

type pendingResolution struct {
	request    PendingRequest
	responseCh chan pendingResolutionResult
}

type pendingResolutionResult struct {
	response PendingResponse
	err      error
}

type interactiveBridge struct {
	mu                sync.Mutex
	pendingByRequest  map[string]*pendingResolution
	toolCallToRequest map[string]string
	permissionQueue   []string
	userInputQueue    []string
	contextMu         sync.RWMutex
	activeCtx         context.Context
}

var errConcurrentUserInputRequestsUnsupported = errors.New("concurrent user input requests are not supported")
var errConcurrentPermissionRequestsUnsupported = errors.New("concurrent permission requests are not supported")

func newInteractiveBridge() *interactiveBridge {
	return &interactiveBridge{
		pendingByRequest:  make(map[string]*pendingResolution),
		toolCallToRequest: make(map[string]string),
	}
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

func (p *copilotProvider) sessionClient() (*copilot.Client, error) {
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		return nil, errors.New("copilot client is not started")
	}
	return client, nil
}

func (p *copilotProvider) NewSession(ctx context.Context, options SessionOptions) (Session, error) {
	client, err := p.sessionClient()
	if err != nil {
		return nil, err
	}
	bridge := newInteractiveBridge()
	permissionMode := options.PermissionMode
	if permissionMode == "" || permissionMode == PermissionModeInherit {
		permissionMode = p.options.PermissionMode
	}
	if permissionMode == "" {
		permissionMode = PermissionModeDeny
	}

	config := &copilot.SessionConfig{
		SessionID:           options.SessionID,
		ClientName:          p.options.ClientName,
		Model:               firstNonEmpty(options.Model, p.options.DefaultModel),
		ConfigDir:           p.options.ConfigDir,
		WorkingDirectory:    p.options.WorkingDirectory,
		Streaming:           true,
		OnPermissionRequest: permissionHandlerForMode(permissionMode, bridge),
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
	if len(options.Tools) > 0 {
		config.Tools = bridge.buildTools(options.Tools)
	}
	if options.Interactive {
		config.OnUserInputRequest = bridge.handleUserInput
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
	return &copilotSession{session: session, bridge: bridge}, nil
}

func (p *copilotProvider) ResumeSession(ctx context.Context, sessionID string, options SessionOptions) (Session, error) {
	client, err := p.sessionClient()
	if err != nil {
		return nil, err
	}
	bridge := newInteractiveBridge()
	permissionMode := options.PermissionMode
	if permissionMode == "" || permissionMode == PermissionModeInherit {
		permissionMode = p.options.PermissionMode
	}
	if permissionMode == "" {
		permissionMode = PermissionModeDeny
	}

	config := &copilot.ResumeSessionConfig{
		ClientName:          p.options.ClientName,
		Model:               firstNonEmpty(options.Model, p.options.DefaultModel),
		ConfigDir:           p.options.ConfigDir,
		WorkingDirectory:    p.options.WorkingDirectory,
		Streaming:           true,
		OnPermissionRequest: permissionHandlerForMode(permissionMode, bridge),
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
	if len(options.Tools) > 0 {
		config.Tools = bridge.buildTools(options.Tools)
	}
	if options.Interactive {
		config.OnUserInputRequest = bridge.handleUserInput
	}

	session, err := client.ResumeSession(ctx, sessionID, config)
	if err != nil {
		return nil, wrapSessionLookupError(fmt.Errorf("resume copilot session %q: %w", sessionID, err))
	}
	if agentName != "" {
		if _, err := session.RPC.Agent.Select(ctx, &rpc.SessionAgentSelectParams{Name: agentName}); err != nil {
			_ = session.Disconnect()
			return nil, fmt.Errorf("select agent %q: %w", agentName, err)
		}
	}
	return &copilotSession{session: session, bridge: bridge}, nil
}

func (p *copilotProvider) DeleteSession(ctx context.Context, sessionID string) error {
	client, err := p.sessionClient()
	if err != nil {
		return err
	}
	if err := client.DeleteSession(ctx, sessionID); err != nil {
		return wrapSessionLookupError(fmt.Errorf("delete copilot session %q: %w", sessionID, err))
	}
	return nil
}

func (s *copilotSession) ID() string {
	return s.session.SessionID
}

func (s *copilotSession) ResolvePending(_ context.Context, response PendingResponse) error {
	if s.bridge == nil {
		return ErrPendingRequestsUnsupported
	}
	return s.bridge.resolve(response)
}

func (s *copilotSession) Close() error {
	return s.session.Disconnect()
}

func (s *copilotSession) Send(ctx context.Context, message MessageOptions, handler EventHandler) (Result, error) {
	sendCtx, cancelSend := context.WithCancelCause(ctx)
	defer cancelSend(nil)
	if s.bridge != nil {
		s.bridge.setActiveContext(sendCtx)
		defer s.bridge.clearActiveContext()
		defer s.bridge.cancelAll(errors.New("pending request cancelled"))
	}

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
		case copilot.ExternalToolRequested:
			if s.bridge != nil {
				s.bridge.registerExternalToolRequest(event.Data)
			}
			dispatch(Event{Type: EventExternalToolRequested, Runtime: &RuntimeEvent{
				Type:      string(EventExternalToolRequested),
				RequestID: deref(event.Data.RequestID),
				CallID:    deref(event.Data.ToolCallID),
				ToolName:  deref(event.Data.ToolName),
				Arguments: event.Data.Arguments,
			}})
		case copilot.ExternalToolCompleted:
			if s.bridge != nil {
				s.bridge.forgetRequest(deref(event.Data.RequestID))
			}
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
			if s.bridge != nil {
				s.bridge.registerPermissionRequest(event.Data)
			}
			dispatch(Event{Type: EventPermissionRequested, Runtime: &RuntimeEvent{
				Type:              string(EventPermissionRequested),
				RequestID:         deref(event.Data.RequestID),
				CallID:            deref(event.Data.ToolCallID),
				PermissionRequest: permissionSummary(event.Data.PermissionRequest),
			}})
		case copilot.PermissionCompleted:
			if s.bridge != nil {
				s.bridge.forgetRequest(deref(event.Data.RequestID))
			}
			dispatch(Event{Type: EventPermissionCompleted, Runtime: &RuntimeEvent{
				Type:      string(EventPermissionCompleted),
				RequestID: deref(event.Data.RequestID),
			}})
		case copilot.UserInputRequested:
			if s.bridge != nil {
				s.bridge.registerUserInputRequest(event.Data)
			}
			dispatch(Event{Type: EventUserInputRequested, Runtime: &RuntimeEvent{
				Type:             string(EventUserInputRequested),
				RequestID:        deref(event.Data.RequestID),
				UserInputRequest: userInputSummary(event.Data),
			}})
		case copilot.UserInputCompleted:
			if s.bridge != nil {
				s.bridge.forgetRequest(deref(event.Data.RequestID))
			}
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

func permissionHandlerForMode(mode PermissionMode, bridge *interactiveBridge) copilot.PermissionHandlerFunc {
	switch mode {
	case PermissionModeAllow:
		return allowAllPermissions
	case PermissionModeBridge:
		return bridge.handlePermission
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

func (b *interactiveBridge) handlePermission(_ copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	pending, err := b.waitForPermissionRequest(b.currentContext())
	if err != nil {
		return copilot.PermissionRequestResult{}, err
	}
	result := <-pending.responseCh
	if result.err != nil {
		return copilot.PermissionRequestResult{}, result.err
	}
	if result.response.Permission == nil {
		return copilot.PermissionRequestResult{}, errors.New("permission continuation missing response")
	}
	return copilot.PermissionRequestResult{
		Kind:  copilot.PermissionRequestResultKind(result.response.Permission.ResultKind),
		Rules: append([]any(nil), result.response.Permission.Rules...),
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

func (b *interactiveBridge) buildTools(definitions []ToolDefinition) []copilot.Tool {
	tools := make([]copilot.Tool, 0, len(definitions))
	for _, definition := range definitions {
		definition := definition
		tools = append(tools, copilot.Tool{
			Name:                 definition.Name,
			Description:          definition.Description,
			Parameters:           cloneMap(definition.Parameters),
			OverridesBuiltInTool: definition.OverrideBuiltIn,
			Handler: func(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
				return b.awaitToolResult(invocation)
			},
		})
	}
	return tools
}

func (b *interactiveBridge) handleUserInput(_ copilot.UserInputRequest, _ copilot.UserInputInvocation) (copilot.UserInputResponse, error) {
	pending, err := b.waitForUserInputRequest(b.currentContext())
	if err != nil {
		return copilot.UserInputResponse{}, err
	}
	result := <-pending.responseCh
	if result.err != nil {
		return copilot.UserInputResponse{}, result.err
	}
	if result.response.UserInput == nil {
		return copilot.UserInputResponse{}, errors.New("user input continuation missing response")
	}
	return copilot.UserInputResponse{
		Answer:      result.response.UserInput.Answer,
		WasFreeform: result.response.UserInput.WasFreeform,
	}, nil
}

func (b *interactiveBridge) awaitToolResult(invocation copilot.ToolInvocation) (copilot.ToolResult, error) {
	pending, err := b.waitForToolRequest(b.currentContext(), invocation.ToolCallID)
	if err != nil {
		return copilot.ToolResult{}, err
	}
	result := <-pending.responseCh
	if result.err != nil {
		return copilot.ToolResult{}, result.err
	}
	if result.response.ToolError != "" {
		return copilot.ToolResult{}, errors.New(result.response.ToolError)
	}
	if result.response.ToolResult == nil {
		return copilot.ToolResult{}, errors.New("tool continuation missing result")
	}
	toolResult := result.response.ToolResult
	copied := copilot.ToolResult{
		TextResultForLLM: toolResult.TextResult,
		ResultType:       toolResult.ResultType,
		SessionLog:       toolResult.SessionLog,
		ToolTelemetry:    cloneMap(toolResult.Telemetry),
	}
	if len(toolResult.BinaryResults) > 0 {
		copied.BinaryResultsForLLM = make([]copilot.ToolBinaryResult, 0, len(toolResult.BinaryResults))
		for _, binary := range toolResult.BinaryResults {
			copied.BinaryResultsForLLM = append(copied.BinaryResultsForLLM, copilot.ToolBinaryResult{
				Data:        binary.Data,
				MimeType:    binary.MIMEType,
				Type:        binary.Type,
				Description: binary.Description,
			})
		}
	}
	return copied, nil
}

func (b *interactiveBridge) registerExternalToolRequest(data copilot.Data) {
	requestID := deref(data.RequestID)
	if requestID == "" {
		return
	}
	pending := &pendingResolution{
		request: PendingRequest{
			ID:        requestID,
			Kind:      PendingRequestTool,
			CallID:    deref(data.ToolCallID),
			ToolName:  deref(data.ToolName),
			Arguments: data.Arguments,
		},
		responseCh: make(chan pendingResolutionResult, 1),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.pendingByRequest[requestID]; ok {
		pending = existing
		pending.request.CallID = deref(data.ToolCallID)
		pending.request.ToolName = deref(data.ToolName)
		pending.request.Arguments = data.Arguments
	} else {
		b.pendingByRequest[requestID] = pending
	}
	if pending.request.CallID != "" {
		b.toolCallToRequest[pending.request.CallID] = requestID
	}
}

func (b *interactiveBridge) registerUserInputRequest(data copilot.Data) {
	requestID := deref(data.RequestID)
	if requestID == "" {
		return
	}
	pending := &pendingResolution{
		request: PendingRequest{
			ID:               requestID,
			Kind:             PendingRequestUserInput,
			UserInputRequest: userInputSummary(data),
		},
		responseCh: make(chan pendingResolutionResult, 1),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.pendingByRequest[requestID]; ok {
		pending = existing
		pending.request.UserInputRequest = userInputSummary(data)
	} else {
		b.pendingByRequest[requestID] = pending
		b.userInputQueue = append(b.userInputQueue, requestID)
		if b.hasOutstandingKindLocked(PendingRequestUserInput, requestID) {
			pending.responseCh <- pendingResolutionResult{err: errConcurrentUserInputRequestsUnsupported}
		}
	}
}

func (b *interactiveBridge) registerPermissionRequest(data copilot.Data) {
	requestID := deref(data.RequestID)
	if requestID == "" {
		return
	}
	pending := &pendingResolution{
		request: PendingRequest{
			ID:                requestID,
			Kind:              PendingRequestPermission,
			CallID:            deref(data.ToolCallID),
			PermissionRequest: permissionSummary(data.PermissionRequest),
		},
		responseCh: make(chan pendingResolutionResult, 1),
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.pendingByRequest[requestID]; ok {
		pending = existing
		pending.request.CallID = deref(data.ToolCallID)
		pending.request.PermissionRequest = permissionSummary(data.PermissionRequest)
	} else {
		b.pendingByRequest[requestID] = pending
		b.permissionQueue = append(b.permissionQueue, requestID)
		if b.hasOutstandingKindLocked(PendingRequestPermission, requestID) {
			pending.responseCh <- pendingResolutionResult{err: errConcurrentPermissionRequestsUnsupported}
		}
	}
}

func (b *interactiveBridge) resolve(response PendingResponse) error {
	requestID := strings.TrimSpace(response.RequestID)
	if requestID == "" {
		return ErrPendingRequestNotFound
	}
	b.mu.Lock()
	pending, ok := b.pendingByRequest[requestID]
	b.mu.Unlock()
	if !ok {
		return ErrPendingRequestNotFound
	}
	if pending.request.Kind != response.Kind {
		return fmt.Errorf("pending request %s expects kind %s", requestID, pending.request.Kind)
	}
	select {
	case pending.responseCh <- pendingResolutionResult{response: response}:
		return nil
	default:
		return fmt.Errorf("pending request %s already resolved", requestID)
	}
}

func (b *interactiveBridge) forgetRequest(requestID string) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	pending, ok := b.pendingByRequest[requestID]
	if !ok {
		return
	}
	delete(b.pendingByRequest, requestID)
	if pending.request.CallID != "" {
		delete(b.toolCallToRequest, pending.request.CallID)
	}
	if pending.request.Kind == PendingRequestUserInput {
		filtered := b.userInputQueue[:0]
		for _, queued := range b.userInputQueue {
			if queued != requestID {
				filtered = append(filtered, queued)
			}
		}
		b.userInputQueue = filtered
	}
	if pending.request.Kind == PendingRequestPermission {
		filtered := b.permissionQueue[:0]
		for _, queued := range b.permissionQueue {
			if queued != requestID {
				filtered = append(filtered, queued)
			}
		}
		b.permissionQueue = filtered
	}
}

func (b *interactiveBridge) cancelAll(err error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for requestID, pending := range b.pendingByRequest {
		select {
		case pending.responseCh <- pendingResolutionResult{err: err}:
		default:
		}
		delete(b.pendingByRequest, requestID)
	}
	clear(b.toolCallToRequest)
	b.permissionQueue = nil
	b.userInputQueue = nil
}

func (b *interactiveBridge) waitForToolRequest(ctx context.Context, toolCallID string) (*pendingResolution, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		requestID := b.toolCallToRequest[toolCallID]
		pending := b.pendingByRequest[requestID]
		b.mu.Unlock()
		if pending != nil {
			return pending, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for tool request %s", toolCallID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *interactiveBridge) waitForUserInputRequest(ctx context.Context) (*pendingResolution, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if len(b.userInputQueue) > 0 {
			requestID := b.userInputQueue[0]
			b.userInputQueue = append([]string(nil), b.userInputQueue[1:]...)
			pending := b.pendingByRequest[requestID]
			b.mu.Unlock()
			if pending != nil {
				return pending, nil
			}
			continue
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for user input request")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *interactiveBridge) waitForPermissionRequest(ctx context.Context) (*pendingResolution, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if len(b.permissionQueue) > 0 {
			requestID := b.permissionQueue[0]
			b.permissionQueue = append([]string(nil), b.permissionQueue[1:]...)
			pending := b.pendingByRequest[requestID]
			b.mu.Unlock()
			if pending != nil {
				return pending, nil
			}
			continue
		}
		b.mu.Unlock()
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for permission request")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (b *interactiveBridge) hasOutstandingKindLocked(kind PendingRequestKind, excludeRequestID string) bool {
	for requestID, pending := range b.pendingByRequest {
		if requestID == excludeRequestID {
			continue
		}
		if pending.request.Kind == kind {
			return true
		}
	}
	return false
}

func (b *interactiveBridge) setActiveContext(ctx context.Context) {
	if b == nil {
		return
	}
	b.contextMu.Lock()
	b.activeCtx = ctx
	b.contextMu.Unlock()
}

func (b *interactiveBridge) clearActiveContext() {
	if b == nil {
		return
	}
	b.contextMu.Lock()
	b.activeCtx = nil
	b.contextMu.Unlock()
}

func (b *interactiveBridge) currentContext() context.Context {
	if b == nil {
		return context.Background()
	}
	b.contextMu.RLock()
	ctx := b.activeCtx
	b.contextMu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func wrapSessionLookupError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrSessionNotFound) {
		return err
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unknown session") ||
		strings.Contains(message, "session not found") ||
		strings.Contains(message, "session does not exist") {
		return fmt.Errorf("%w: %v", ErrSessionNotFound, err)
	}
	return err
}
