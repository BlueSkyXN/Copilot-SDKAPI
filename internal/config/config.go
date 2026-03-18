package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	gatewayruntime "copilot-sdkapi/internal/runtime"
)

const (
	defaultListenAddr        = ":38095"
	defaultModel             = "gpt-4.1"
	defaultSessionTTL        = 15 * time.Minute
	defaultStreamIdleTimout  = 2 * time.Minute
	defaultLogLevel          = "error"
	defaultClientName        = "copilot-sdkapi"
	defaultMaxBodyBytes      = 1 << 20
	defaultMaxActiveSessions = 256
	defaultMaxSessionsPerKey = 64
	defaultAPIKeyLabel       = "default"
	defaultAPIKeySecret      = "test-key"
)

type Config struct {
	ListenAddr             string
	DefaultModel           string
	WorkingDirectory       string
	ConfigDir              string
	SessionStorePath       string
	RequestTimeout         time.Duration
	SessionTTL             time.Duration
	StreamIdleTimeout      time.Duration
	LogLevel               string
	ClientName             string
	CLIPath                string
	GitHubToken            string
	UseLoggedInUser        bool
	MaxBodyBytes           int64
	AllowPrivateRemoteURLs bool
	MaxActiveSessions      int
	MaxSessionsPerKey      int
	SDKAvailableTools      []string
	SDKExcludedTools       []string
	SDKSkillDirs           []string
	SDKDisabledSkills      []string
	SDKMCPServers          map[string]map[string]any
	SDKCustomAgents        []gatewayruntime.CustomAgent
	SDKDefaultAgent        string
	SDKPermissionMode      gatewayruntime.PermissionMode
	SDKInfiniteSessions    *gatewayruntime.InfiniteSessionOptions
	APIKeys                []APIKey
	UsingDefaultAPIKey     bool
}

type APIKey struct {
	Label  string
	Secret string
}

func Load() (Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("resolve working directory: %w", err)
	}

	cfg := Config{
		ListenAddr:             defaultListenAddr,
		DefaultModel:           defaultModel,
		WorkingDirectory:       wd,
		SessionStorePath:       defaultSessionStorePath(),
		RequestTimeout:         defaultStreamIdleTimout,
		SessionTTL:             defaultSessionTTL,
		StreamIdleTimeout:      defaultStreamIdleTimout,
		LogLevel:               defaultLogLevel,
		ClientName:             defaultClientName,
		MaxBodyBytes:           defaultMaxBodyBytes,
		AllowPrivateRemoteURLs: false,
		MaxActiveSessions:      defaultMaxActiveSessions,
		MaxSessionsPerKey:      defaultMaxSessionsPerKey,
		SDKPermissionMode:      gatewayruntime.PermissionModeDeny,
	}

	if value := strings.TrimSpace(os.Getenv("GATEWAY_LISTEN_ADDR")); value != "" {
		cfg.ListenAddr = normalizeListenAddr(value)
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_DEFAULT_MODEL")); value != "" {
		cfg.DefaultModel = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_WORKING_DIR")); value != "" {
		cfg.WorkingDirectory = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_CONFIG_DIR")); value != "" {
		cfg.ConfigDir = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_SESSION_STORE_PATH")); value != "" {
		cfg.SessionStorePath = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_LOG_LEVEL")); value != "" {
		cfg.LogLevel = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_CLIENT_NAME")); value != "" {
		cfg.ClientName = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_CLI_PATH")); value != "" {
		cfg.CLIPath = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_GITHUB_TOKEN")); value != "" {
		cfg.GitHubToken = value
	}
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = strings.TrimSpace(os.Getenv("COPILOT_GITHUB_TOKEN"))
	}
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	if cfg.GitHubToken == "" {
		cfg.GitHubToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}

	if value := strings.TrimSpace(os.Getenv("GATEWAY_SESSION_TTL")); value != "" {
		cfg.SessionTTL, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_SESSION_TTL: %w", err)
		}
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_STREAM_IDLE_TIMEOUT")); value != "" {
		cfg.StreamIdleTimeout, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_STREAM_IDLE_TIMEOUT: %w", err)
		}
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_REQUEST_TIMEOUT")); value != "" {
		cfg.RequestTimeout, err = time.ParseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_REQUEST_TIMEOUT: %w", err)
		}
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_MAX_BODY_BYTES")); value != "" {
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_MAX_BODY_BYTES: %w", parseErr)
		}
		cfg.MaxBodyBytes = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_ALLOW_PRIVATE_REMOTE_URLS")); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_ALLOW_PRIVATE_REMOTE_URLS: %w", parseErr)
		}
		cfg.AllowPrivateRemoteURLs = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_MAX_ACTIVE_SESSIONS")); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_MAX_ACTIVE_SESSIONS: %w", parseErr)
		}
		cfg.MaxActiveSessions = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY")); value != "" {
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY: %w", parseErr)
		}
		cfg.MaxSessionsPerKey = parsed
	}
	cfg.SDKAvailableTools = parseCSV(os.Getenv("GATEWAY_SDK_AVAILABLE_TOOLS"))
	cfg.SDKExcludedTools = parseCSV(os.Getenv("GATEWAY_SDK_EXCLUDED_TOOLS"))
	cfg.SDKSkillDirs = parseCSV(os.Getenv("GATEWAY_SDK_SKILL_DIRECTORIES"))
	cfg.SDKDisabledSkills = parseCSV(os.Getenv("GATEWAY_SDK_DISABLED_SKILLS"))
	if value := strings.TrimSpace(os.Getenv("GATEWAY_SDK_MCP_SERVERS_JSON")); value != "" {
		parsed, parseErr := parseJSONMap(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_SDK_MCP_SERVERS_JSON: %w", parseErr)
		}
		cfg.SDKMCPServers = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_SDK_CUSTOM_AGENTS_JSON")); value != "" {
		parsed, parseErr := parseCustomAgents(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_SDK_CUSTOM_AGENTS_JSON: %w", parseErr)
		}
		cfg.SDKCustomAgents = parsed
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_SDK_DEFAULT_AGENT")); value != "" {
		cfg.SDKDefaultAgent = value
	}
	if value := strings.TrimSpace(os.Getenv("GATEWAY_SDK_PERMISSION_MODE")); value != "" {
		permissionMode, parseErr := parsePermissionMode(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_SDK_PERMISSION_MODE: %w", parseErr)
		}
		cfg.SDKPermissionMode = permissionMode
	}
	infiniteSessions, err := parseInfiniteSessionsFromEnv()
	if err != nil {
		return Config{}, err
	}
	cfg.SDKInfiniteSessions = infiniteSessions
	if err := validateCustomAgents(cfg.SDKCustomAgents, cfg.SDKDefaultAgent); err != nil {
		return Config{}, err
	}

	useLoggedInUser := cfg.GitHubToken == ""
	if value := strings.TrimSpace(os.Getenv("GATEWAY_USE_LOGGED_IN_USER")); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return Config{}, fmt.Errorf("parse GATEWAY_USE_LOGGED_IN_USER: %w", parseErr)
		}
		useLoggedInUser = parsed
	}
	cfg.UseLoggedInUser = useLoggedInUser

	apiKeys, usingDefaultAPIKey, err := parseAPIKeys(os.Getenv("GATEWAY_API_KEYS"))
	if err != nil {
		return Config{}, err
	}
	cfg.APIKeys = apiKeys
	cfg.UsingDefaultAPIKey = usingDefaultAPIKey

	return cfg, nil
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			items = append(items, part)
		}
	}
	return items
}

func parseJSONMap(raw string) (map[string]map[string]any, error) {
	var parsed map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseCustomAgents(raw string) ([]gatewayruntime.CustomAgent, error) {
	var parsed []gatewayruntime.CustomAgent
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func parsePermissionMode(raw string) (gatewayruntime.PermissionMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(gatewayruntime.PermissionModeDeny):
		return gatewayruntime.PermissionModeDeny, nil
	case string(gatewayruntime.PermissionModeBridge):
		return gatewayruntime.PermissionModeBridge, nil
	case string(gatewayruntime.PermissionModeAllow):
		return gatewayruntime.PermissionModeAllow, nil
	default:
		return "", fmt.Errorf("unsupported permission mode %q", raw)
	}
}

func parseInfiniteSessionsFromEnv() (*gatewayruntime.InfiniteSessionOptions, error) {
	var (
		enabledRaw    = strings.TrimSpace(os.Getenv("GATEWAY_SDK_INFINITE_SESSIONS_ENABLED"))
		backgroundRaw = strings.TrimSpace(os.Getenv("GATEWAY_SDK_INFINITE_SESSIONS_BACKGROUND_COMPACTION_THRESHOLD"))
		bufferRaw     = strings.TrimSpace(os.Getenv("GATEWAY_SDK_INFINITE_SESSIONS_BUFFER_EXHAUSTION_THRESHOLD"))
	)
	if enabledRaw == "" && backgroundRaw == "" && bufferRaw == "" {
		return nil, nil
	}

	cfg := &gatewayruntime.InfiniteSessionOptions{}
	if enabledRaw != "" {
		parsed, err := strconv.ParseBool(enabledRaw)
		if err != nil {
			return nil, fmt.Errorf("parse GATEWAY_SDK_INFINITE_SESSIONS_ENABLED: %w", err)
		}
		cfg.Enabled = &parsed
	}
	if backgroundRaw != "" {
		parsed, err := parseThreshold(backgroundRaw)
		if err != nil {
			return nil, fmt.Errorf("parse GATEWAY_SDK_INFINITE_SESSIONS_BACKGROUND_COMPACTION_THRESHOLD: %w", err)
		}
		cfg.BackgroundCompactionThreshold = &parsed
	}
	if bufferRaw != "" {
		parsed, err := parseThreshold(bufferRaw)
		if err != nil {
			return nil, fmt.Errorf("parse GATEWAY_SDK_INFINITE_SESSIONS_BUFFER_EXHAUSTION_THRESHOLD: %w", err)
		}
		cfg.BufferExhaustionThreshold = &parsed
	}
	return cfg, nil
}

func parseThreshold(raw string) (float64, error) {
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if parsed < 0 || parsed > 1 {
		return 0, fmt.Errorf("threshold must be between 0 and 1")
	}
	return parsed, nil
}

func defaultSessionStorePath() string {
	baseDir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(baseDir) == "" {
		baseDir = os.TempDir()
	}
	return filepath.Join(baseDir, "copilot-sdkapi", "sessions.json")
}

func validateCustomAgents(agents []gatewayruntime.CustomAgent, defaultAgent string) error {
	if len(agents) == 0 {
		if strings.TrimSpace(defaultAgent) != "" {
			return fmt.Errorf("GATEWAY_SDK_DEFAULT_AGENT requires GATEWAY_SDK_CUSTOM_AGENTS_JSON")
		}
		return nil
	}
	seen := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			return fmt.Errorf("custom agent name must not be empty")
		}
		if strings.TrimSpace(agent.Prompt) == "" {
			return fmt.Errorf("custom agent %q must include prompt", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate custom agent %q", name)
		}
		seen[name] = struct{}{}
	}
	if strings.TrimSpace(defaultAgent) == "" {
		return nil
	}
	if _, ok := seen[strings.TrimSpace(defaultAgent)]; !ok {
		return fmt.Errorf("default custom agent %q is not defined", defaultAgent)
	}
	return nil
}

func normalizeListenAddr(value string) string {
	if value == "" {
		return defaultListenAddr
	}
	if strings.HasPrefix(value, ":") || strings.Contains(value, ":") {
		return value
	}
	return ":" + value
}

func parseAPIKeys(raw string) ([]APIKey, bool, error) {
	if strings.TrimSpace(raw) == "" {
		return []APIKey{{
			Label:  defaultAPIKeyLabel,
			Secret: defaultAPIKeySecret,
		}}, true, nil
	}

	parts := strings.Split(raw, ",")
	keys := make([]APIKey, 0, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label := fmt.Sprintf("key-%d", index+1)
		secret := part
		if left, right, found := strings.Cut(part, "="); found {
			label = strings.TrimSpace(left)
			secret = strings.TrimSpace(right)
		}
		if label == "" || secret == "" {
			return nil, false, fmt.Errorf("invalid GATEWAY_API_KEYS entry %q", part)
		}
		keys = append(keys, APIKey{Label: label, Secret: secret})
	}
	if len(keys) == 0 {
		return nil, false, fmt.Errorf("GATEWAY_API_KEYS must contain at least one API key")
	}
	return keys, false, nil
}
