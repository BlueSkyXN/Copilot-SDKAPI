package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadParsesConfiguration(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1,two=key2")
	t.Setenv("GATEWAY_LISTEN_ADDR", "9090")
	t.Setenv("GATEWAY_DEFAULT_MODEL", "claude-sonnet-4.5")
	t.Setenv("GATEWAY_REQUEST_TIMEOUT", "3m")
	t.Setenv("GATEWAY_STREAM_IDLE_TIMEOUT", "45s")
	t.Setenv("GATEWAY_SESSION_TTL", "20m")
	t.Setenv("GATEWAY_SESSION_STORE_PATH", "/tmp/copilot-sdkapi-test-sessions.json")
	t.Setenv("GATEWAY_MAX_ACTIVE_SESSIONS", "123")
	t.Setenv("GATEWAY_MAX_ACTIVE_SESSIONS_PER_KEY", "17")
	t.Setenv("GATEWAY_ALLOW_PRIVATE_REMOTE_URLS", "true")
	t.Setenv("GATEWAY_USE_LOGGED_IN_USER", "false")
	t.Setenv("GATEWAY_SDK_AVAILABLE_TOOLS", "view,edit")
	t.Setenv("GATEWAY_SDK_EXCLUDED_TOOLS", "shell")
	t.Setenv("GATEWAY_SDK_SKILL_DIRECTORIES", "./skills/a, ./skills/b")
	t.Setenv("GATEWAY_SDK_DISABLED_SKILLS", "beta,legacy")
	t.Setenv("GATEWAY_SDK_MCP_SERVERS_JSON", `{"filesystem":{"type":"local","command":"node","args":["./mcp.js"]}}`)
	t.Setenv("GATEWAY_SDK_CUSTOM_AGENTS_JSON", `[{"name":"reviewer","displayName":"Reviewer","prompt":"Review carefully.","tools":["view"]}]`)
	t.Setenv("GATEWAY_SDK_DEFAULT_AGENT", "reviewer")
	t.Setenv("GATEWAY_SDK_PERMISSION_MODE", "allow")
	t.Setenv("GATEWAY_SDK_INFINITE_SESSIONS_ENABLED", "false")
	t.Setenv("GATEWAY_SDK_INFINITE_SESSIONS_BACKGROUND_COMPACTION_THRESHOLD", "0.7")
	t.Setenv("GATEWAY_SDK_INFINITE_SESSIONS_BUFFER_EXHAUSTION_THRESHOLD", "0.9")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != ":9090" {
		t.Fatalf("expected normalized listen addr, got %q", cfg.ListenAddr)
	}
	if cfg.DefaultModel != "claude-sonnet-4.5" {
		t.Fatalf("unexpected default model %q", cfg.DefaultModel)
	}
	if cfg.RequestTimeout != 3*time.Minute {
		t.Fatalf("unexpected request timeout %s", cfg.RequestTimeout)
	}
	if cfg.StreamIdleTimeout != 45*time.Second {
		t.Fatalf("unexpected stream idle timeout %s", cfg.StreamIdleTimeout)
	}
	if cfg.SessionTTL != 20*time.Minute {
		t.Fatalf("unexpected session ttl %s", cfg.SessionTTL)
	}
	if cfg.SessionStorePath != "/tmp/copilot-sdkapi-test-sessions.json" {
		t.Fatalf("unexpected session store path %q", cfg.SessionStorePath)
	}
	if cfg.UseLoggedInUser {
		t.Fatalf("expected logged in user auth to be disabled")
	}
	if cfg.MaxActiveSessions != 123 {
		t.Fatalf("unexpected max active sessions %d", cfg.MaxActiveSessions)
	}
	if cfg.MaxSessionsPerKey != 17 {
		t.Fatalf("unexpected max sessions per key %d", cfg.MaxSessionsPerKey)
	}
	if !cfg.AllowPrivateRemoteURLs {
		t.Fatalf("expected private remote URLs to be enabled")
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("expected 2 api keys, got %d", len(cfg.APIKeys))
	}
	if cfg.UsingDefaultAPIKey {
		t.Fatalf("expected explicit api keys to disable default fallback")
	}
	if len(cfg.SDKAvailableTools) != 2 || cfg.SDKAvailableTools[0] != "view" || cfg.SDKAvailableTools[1] != "edit" {
		t.Fatalf("unexpected SDK available tools %#v", cfg.SDKAvailableTools)
	}
	if len(cfg.SDKExcludedTools) != 1 || cfg.SDKExcludedTools[0] != "shell" {
		t.Fatalf("unexpected SDK excluded tools %#v", cfg.SDKExcludedTools)
	}
	if len(cfg.SDKSkillDirs) != 2 || cfg.SDKSkillDirs[0] != "./skills/a" || cfg.SDKSkillDirs[1] != "./skills/b" {
		t.Fatalf("unexpected SDK skill dirs %#v", cfg.SDKSkillDirs)
	}
	if len(cfg.SDKDisabledSkills) != 2 || cfg.SDKDisabledSkills[0] != "beta" || cfg.SDKDisabledSkills[1] != "legacy" {
		t.Fatalf("unexpected SDK disabled skills %#v", cfg.SDKDisabledSkills)
	}
	if len(cfg.SDKMCPServers) != 1 || cfg.SDKMCPServers["filesystem"]["type"] != "local" {
		t.Fatalf("unexpected SDK mcp servers %#v", cfg.SDKMCPServers)
	}
	if len(cfg.SDKCustomAgents) != 1 || cfg.SDKCustomAgents[0].Name != "reviewer" || cfg.SDKDefaultAgent != "reviewer" {
		t.Fatalf("unexpected custom agents %#v / default agent %q", cfg.SDKCustomAgents, cfg.SDKDefaultAgent)
	}
	if cfg.SDKPermissionMode != "allow" {
		t.Fatalf("unexpected permission mode %q", cfg.SDKPermissionMode)
	}
	if cfg.SDKInfiniteSessions == nil || cfg.SDKInfiniteSessions.Enabled == nil || *cfg.SDKInfiniteSessions.Enabled || cfg.SDKInfiniteSessions.BackgroundCompactionThreshold == nil || *cfg.SDKInfiniteSessions.BackgroundCompactionThreshold != 0.7 || cfg.SDKInfiniteSessions.BufferExhaustionThreshold == nil || *cfg.SDKInfiniteSessions.BufferExhaustionThreshold != 0.9 {
		t.Fatalf("unexpected infinite session config %#v", cfg.SDKInfiniteSessions)
	}
}

func TestLoadParsesBridgePermissionMode(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("GATEWAY_SDK_PERMISSION_MODE", "bridge")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.SDKPermissionMode != "bridge" {
		t.Fatalf("unexpected permission mode %q", cfg.SDKPermissionMode)
	}
}

func TestLoadFallsBackToDefaultTrialAPIKeyWhenUnset(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != defaultListenAddr {
		t.Fatalf("unexpected default listen addr %q", cfg.ListenAddr)
	}
	if !cfg.UsingDefaultAPIKey {
		t.Fatalf("expected default api key fallback to be enabled")
	}
	if len(cfg.APIKeys) != 1 {
		t.Fatalf("expected 1 default api key, got %d", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].Label != defaultAPIKeyLabel {
		t.Fatalf("unexpected default api key label %q", cfg.APIKeys[0].Label)
	}
	if cfg.APIKeys[0].Secret != defaultAPIKeySecret {
		t.Fatalf("unexpected default api key secret %q", cfg.APIKeys[0].Secret)
	}
}

func TestLoadPrefersGatewayGitHubTokenOverFallbacks(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("GATEWAY_GITHUB_TOKEN", "gateway-token")
	t.Setenv("COPILOT_GITHUB_TOKEN", "copilot-token")
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("GITHUB_TOKEN", "github-token")
	t.Setenv("GATEWAY_USE_LOGGED_IN_USER", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.GitHubToken != "gateway-token" {
		t.Fatalf("expected gateway token to win, got %q", cfg.GitHubToken)
	}
	if cfg.UseLoggedInUser {
		t.Fatalf("expected explicit token to disable logged-in-user fallback")
	}
}

func TestLoadFallsBackToCopilotGitHubToken(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("COPILOT_GITHUB_TOKEN", "copilot-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.GitHubToken != "copilot-token" {
		t.Fatalf("expected COPILOT_GITHUB_TOKEN fallback, got %q", cfg.GitHubToken)
	}
}

func TestLoadRejectsInvalidRequestTimeout(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("GATEWAY_REQUEST_TIMEOUT", "not-a-duration")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "parse GATEWAY_REQUEST_TIMEOUT") {
		t.Fatalf("expected invalid request timeout error, got %v", err)
	}
}

func TestLoadRejectsInvalidPermissionMode(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("GATEWAY_SDK_PERMISSION_MODE", "maybe")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "parse GATEWAY_SDK_PERMISSION_MODE") {
		t.Fatalf("expected invalid permission mode error, got %v", err)
	}
}

func TestLoadRejectsInvalidCustomAgentDefinition(t *testing.T) {
	t.Setenv("GATEWAY_API_KEYS", "one=key1")
	t.Setenv("GATEWAY_SDK_CUSTOM_AGENTS_JSON", `[{"name":"reviewer"}]`)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), `must include prompt`) {
		t.Fatalf("expected invalid custom agent error, got %v", err)
	}
}
