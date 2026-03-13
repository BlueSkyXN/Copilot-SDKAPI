package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"copilot-sdkapi/internal/auth"
	"copilot-sdkapi/internal/config"
	gatewayruntime "copilot-sdkapi/internal/runtime"
	"copilot-sdkapi/internal/session"
	"copilot-sdkapi/internal/usage"
)

const tinyPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg=="

func decodeTinyPNG(t *testing.T) []byte {
	t.Helper()
	decoded, err := base64.StdEncoding.DecodeString(tinyPNGBase64)
	if err != nil {
		t.Fatalf("decode tiny png: %v", err)
	}
	return decoded
}

func TestHandleOpenAIChatCompletionsJSON(t *testing.T) {
	provider := &fakeProvider{
		models: []gatewayruntime.Model{{ID: "gpt-4.1", Name: "GPT-4.1"}},
		nextSession: &fakeSession{
			id:       "session-json",
			response: "Hello from Copilot",
			usage: gatewayruntime.Usage{
				InputTokens:  11,
				OutputTokens: 5,
			},
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"gpt-4.1","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"Hello"}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Session-ID") != "" {
		t.Fatalf("expected stateless response to omit session header, got %q", recorder.Header().Get("X-Session-ID"))
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["object"] != "chat.completion" {
		t.Fatalf("unexpected response object: %#v", response["object"])
	}

	if len(provider.createdSessions) != 1 {
		t.Fatalf("expected 1 created session, got %d", len(provider.createdSessions))
	}
	if provider.createdSessions[0].prompts[0] == "Hello" {
		t.Fatalf("expected stateless request to include transcript, got only latest user prompt")
	}
}

func TestHandleOpenAIChatCompletionsStream(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:       "session-stream",
			deltas:   []string{"Hel", "lo"},
			response: "Hello",
			usage: gatewayruntime.Usage{
				InputTokens:  9,
				OutputTokens: 5,
			},
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Hello"}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	output := recorder.Body.String()
	if !strings.Contains(output, `"role":"assistant"`) {
		t.Fatalf("expected assistant role chunk, got %s", output)
	}
	if !strings.Contains(output, `"content":"Hel"`) || !strings.Contains(output, `"content":"lo"`) {
		t.Fatalf("expected streaming delta chunks, got %s", output)
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Fatalf("expected [DONE] terminator, got %s", output)
	}
}

func TestHandleClaudeMessagesStreamErrorEndsCleanly(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:      "session-claude-stream",
			deltas:  []string{"Hel"},
			sendErr: &gatewayruntime.ResponseError{Type: "runtime_error", Message: "boom", StatusCode: http.StatusBadGateway},
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"claude-sonnet-4.5","stream":true,"messages":[{"role":"user","content":"Hello"}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer test-key",
		"anthropic-version": "2023-06-01",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected streaming response to keep 200 status once headers are written, got %d: %s", recorder.Code, recorder.Body.String())
	}

	output := recorder.Body.String()
	if !strings.Contains(output, "event: error") {
		t.Fatalf("expected Claude error event, got %s", output)
	}
	if !strings.Contains(output, "event: content_block_stop") || !strings.Contains(output, "event: message_stop") {
		t.Fatalf("expected Claude stream to terminate cleanly after error, got %s", output)
	}
}

func TestHandleClaudeMessagesJSON(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:       "session-claude",
			response: "Claude reply",
			usage: gatewayruntime.Usage{
				InputTokens:      10,
				OutputTokens:     6,
				CacheReadTokens:  2,
				CacheWriteTokens: 1,
			},
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hi"}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer test-key",
		"anthropic-version": "2023-06-01",
	})

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("X-Session-ID") != "" {
		t.Fatalf("expected stateless response to omit session header, got %q", recorder.Header().Get("X-Session-ID"))
	}

	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["type"] != "message" {
		t.Fatalf("unexpected response type: %#v", response["type"])
	}
}

func TestHandleClaudeMessagesRequiresAnthropicVersion(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/messages", `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hi"}]}`, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when anthropic-version is missing, got %d", recorder.Code)
	}
}

func TestHandleClaudeMessagesRejectsUnsupportedAnthropicVersion(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/messages", `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hi"}]}`, map[string]string{
		"Authorization":     "Bearer test-key",
		"anthropic-version": "2024-01-01",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when anthropic-version is unsupported, got %d", recorder.Code)
	}
}

func TestSessionReuseUsesLatestUserPrompt(t *testing.T) {
	provider := &fakeProvider{
		sequence: []*fakeSession{
			{id: "persisted-1", response: "First answer"},
		},
	}
	server := newTestServer(t, provider)

	first := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi!"},{"role":"user","content":"Tell me a joke"}]}`
	firstRecorder := performRequest(server, http.MethodPost, "/v1/chat/completions", first, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-key",
	})
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", firstRecorder.Code)
	}
	if firstRecorder.Header().Get("X-Session-ID") != "session-key" {
		t.Fatalf("expected public session key to be echoed back, got %q", firstRecorder.Header().Get("X-Session-ID"))
	}

	second := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi!"},{"role":"user","content":"Tell me a joke"},{"role":"assistant","content":"Why did the model cross the road?"},{"role":"user","content":"Explain it"}]}`
	secondRecorder := performRequest(server, http.MethodPost, "/v1/chat/completions", second, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-key",
	})
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("expected second request to succeed, got %d", secondRecorder.Code)
	}

	if len(provider.createdSessions) != 1 {
		t.Fatalf("expected only one session to be created, got %d", len(provider.createdSessions))
	}
	prompts := provider.createdSessions[0].prompts
	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(prompts))
	}
	if prompts[1] != "Explain it" {
		t.Fatalf("expected reused session to send latest user prompt, got %q", prompts[1])
	}
}

func TestSessionReuseResumesAcrossServerRestart(t *testing.T) {
	storePath := t.TempDir() + "/sessions.json"
	provider := &fakeProvider{
		nextSession: &fakeSession{response: "First answer"},
	}

	firstServer := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.SessionStorePath = storePath
	})

	first := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi!"},{"role":"user","content":"Tell me a joke"}]}`
	firstRecorder := performRequest(firstServer, http.MethodPost, "/v1/chat/completions", first, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-key",
	})
	if firstRecorder.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", firstRecorder.Code)
	}

	secondServer := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.SessionStorePath = storePath
	})

	second := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi!"},{"role":"user","content":"Tell me a joke"},{"role":"assistant","content":"Why did the model cross the road?"},{"role":"user","content":"Explain it"}]}`
	secondRecorder := performRequest(secondServer, http.MethodPost, "/v1/chat/completions", second, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-key",
	})
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("expected resumed request to succeed, got %d", secondRecorder.Code)
	}

	if len(provider.createdSessions) != 1 {
		t.Fatalf("expected only one session creation across restart, got %d", len(provider.createdSessions))
	}
	if len(provider.resumedSessions) != 1 {
		t.Fatalf("expected one resumed session across restart, got %d", len(provider.resumedSessions))
	}
	prompts := provider.createdSessions[0].prompts
	if len(prompts) != 2 {
		t.Fatalf("expected 2 prompts after restart resume, got %d", len(prompts))
	}
	if prompts[1] != "Explain it" {
		t.Fatalf("expected resumed session to send latest user prompt, got %q", prompts[1])
	}
}

func TestInteractiveCopilotFlowRequiresStreaming(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"}],"x_copilot":{"interactive":true}}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "interactive-session",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-streaming interactive flow, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "stream=true") {
		t.Fatalf("expected interactive validation error, got %s", recorder.Body.String())
	}
}

func TestInteractiveCopilotFlowRequiresSessionID(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Hello"}],"x_copilot":{"interactive":true}}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for interactive flow without X-Session-ID, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "X-Session-ID") {
		t.Fatalf("expected missing session ID validation error, got %s", recorder.Body.String())
	}
}

func TestPermissionBridgeRequiresSessionID(t *testing.T) {
	server := newTestServerWithConfig(t, &fakeProvider{}, func(cfg *config.Config) {
		cfg.SDKPermissionMode = gatewayruntime.PermissionModeBridge
	})
	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Hello"}],"x_copilot":{"permission_mode":"bridge"}}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for permission bridge without X-Session-ID, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "X-Session-ID") {
		t.Fatalf("expected missing session ID validation error, got %s", recorder.Body.String())
	}
}

func TestPermissionModeCannotEscalateBeyondServerPolicy(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Hello"}],"x_copilot":{"permission_mode":"allow"}}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "permission-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for permission escalation, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "cannot exceed server permission policy") {
		t.Fatalf("expected escalation validation error, got %s", recorder.Body.String())
	}
}

func TestInteractiveToolBridgeStreamsPendingRequestAndResumes(t *testing.T) {
	provider := &bridgeProvider{session: newBridgeSession("bridge-session")}
	server := newTestServer(t, provider)
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Use the tool"}],"x_copilot":{"interactive":true,"tools":[{"name":"lookup_issue","description":"Look up an issue","parameters":{"type":"object","properties":{"id":{"type":"string"}}}}]}}`
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "bridge-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- string(payload)
	}()

	select {
	case <-provider.session.requested:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool request")
	}

	continuation := `{"request_id":"req-1","kind":"tool_result","text_result":"Issue 123","result_type":"success"}`
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/copilot/respond", bytes.NewBufferString(continuation))
	if err != nil {
		t.Fatalf("create continuation request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "bridge-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send continuation request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected continuation to succeed, got %d", response.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatalf("stream request failed: %v", err)
	case payload := <-resultCh:
		if !strings.Contains(payload, `"type":"external_tool_requested"`) {
			t.Fatalf("expected stream to contain external tool request, got %s", payload)
		}
		if !strings.Contains(payload, `"request_id":"req-1"`) {
			t.Fatalf("expected stream to contain request ID, got %s", payload)
		}
		if !strings.Contains(payload, `"Done after tool"`) {
			t.Fatalf("expected stream to continue after continuation, got %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for streamed response")
	}

	if len(provider.sessionOptions) != 1 {
		t.Fatalf("expected one session options entry, got %d", len(provider.sessionOptions))
	}
	if !provider.sessionOptions[0].Interactive || len(provider.sessionOptions[0].Tools) != 1 || provider.sessionOptions[0].Tools[0].Name != "lookup_issue" {
		t.Fatalf("unexpected session options %#v", provider.sessionOptions[0])
	}
}

func TestPermissionBridgeStreamsPendingRequestAndResumes(t *testing.T) {
	provider := &permissionBridgeProvider{session: newPermissionBridgeSession("permission-session")}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.SDKPermissionMode = gatewayruntime.PermissionModeBridge
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Need approval"}],"x_copilot":{"permission_mode":"bridge"}}`
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "permission-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- string(payload)
	}()

	select {
	case <-provider.session.requested:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for permission request")
	}

	continuation := `{"request_id":"perm-1","kind":"permission_result","result_type":"approved"}`
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/copilot/respond", bytes.NewBufferString(continuation))
	if err != nil {
		t.Fatalf("create continuation request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "permission-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send continuation request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected continuation to succeed, got %d", response.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatalf("stream request failed: %v", err)
	case payload := <-resultCh:
		if !strings.Contains(payload, `"type":"permission_requested"`) {
			t.Fatalf("expected stream to contain permission request, got %s", payload)
		}
		if !strings.Contains(payload, `"request_id":"perm-1"`) {
			t.Fatalf("expected stream to contain request ID, got %s", payload)
		}
		if !strings.Contains(payload, `"Done after permission"`) {
			t.Fatalf("expected stream to continue after permission continuation, got %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for streamed response")
	}

	if len(provider.sessionOptions) != 1 || provider.sessionOptions[0].PermissionMode != gatewayruntime.PermissionModeBridge {
		t.Fatalf("unexpected session options %#v", provider.sessionOptions)
	}
}

func TestContinuationRefreshesStreamIdleTimeout(t *testing.T) {
	provider := &permissionBridgeProvider{session: &permissionBridgeSession{
		id:                 "permission-session",
		requested:          make(chan struct{}, 1),
		responses:          make(chan gatewayruntime.PendingResponse, 1),
		afterResponseDelay: 110 * time.Millisecond,
	}}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.SDKPermissionMode = gatewayruntime.PermissionModeBridge
		cfg.StreamIdleTimeout = 150 * time.Millisecond
		cfg.RequestTimeout = 2 * time.Second
	})
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"Need approval"}],"x_copilot":{"permission_mode":"bridge"}}`
	resultCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/chat/completions", bytes.NewBufferString(body))
		if err != nil {
			errCh <- err
			return
		}
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-ID", "permission-key")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		payload, err := io.ReadAll(resp.Body)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- string(payload)
	}()

	select {
	case <-provider.session.requested:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for permission request")
	}

	time.Sleep(80 * time.Millisecond)

	continuation := `{"request_id":"perm-1","kind":"permission_result","result_type":"approved"}`
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/copilot/respond", bytes.NewBufferString(continuation))
	if err != nil {
		t.Fatalf("create continuation request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "permission-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send continuation request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected continuation to succeed, got %d", response.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatalf("stream request failed: %v", err)
	case payload := <-resultCh:
		if !strings.Contains(payload, `"Done after permission"`) {
			t.Fatalf("expected stream to survive idle timeout after continuation, got %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for streamed response")
	}
}

func TestXCopilotAttachmentsAreMaterializedToSession(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:       "session-attachments",
			response: "Attached",
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Use the attached file"}],"x_copilot":{"attachments":[{"name":"notes.txt","text":"hello world","media_type":"text/plain"}]}}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected attachment request to succeed, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(provider.createdSessions) != 1 || len(provider.createdSessions[0].attachments) != 1 || len(provider.createdSessions[0].attachments[0]) != 1 {
		t.Fatalf("expected one materialized attachment, got %#v", provider.createdSessions)
	}
	attachment := provider.createdSessions[0].attachments[0][0]
	content := provider.createdSessions[0].attachmentData[0][0]
	if string(content) != "hello world" {
		t.Fatalf("unexpected attachment contents %q", string(content))
	}
	if attachment.MediaType != "text/plain" {
		t.Fatalf("unexpected attachment media type %q", attachment.MediaType)
	}
}

func TestOpenAIImageURLIsFetchedAndMaterialized(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(decodeTinyPNG(t))
	}))
	defer imageServer.Close()

	provider := &fakeProvider{
		models: []gatewayruntime.Model{{
			ID:   "gpt-4.1",
			Name: "GPT-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
			Limits: gatewayruntime.ModelLimits{
				Vision: &gatewayruntime.ModelVisionLimits{
					SupportedMediaTypes: []string{"image/png"},
					MaxPromptImages:     4,
					MaxPromptImageSize:  1 << 20,
				},
			},
		}},
		nextSession: &fakeSession{
			id:       "session-image-url",
			response: "Image ok",
		},
	}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.AllowPrivateRemoteURLs = true
	})

	body := fmt.Sprintf(`{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"text","text":"What is in this image?"},{"type":"image_url","image_url":{"url":"%s/image.png"}}]}]}`, imageServer.URL)
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected remote image request to succeed, got %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(provider.createdSessions) != 1 || len(provider.createdSessions[0].attachments) != 1 || len(provider.createdSessions[0].attachments[0]) != 1 {
		t.Fatalf("expected one fetched image attachment, got %#v", provider.createdSessions)
	}
	attachment := provider.createdSessions[0].attachments[0][0]
	if attachment.MediaType != "image/png" {
		t.Fatalf("unexpected fetched image media type %q", attachment.MediaType)
	}
	content := provider.createdSessions[0].attachmentData[0][0]
	if len(content) == 0 {
		t.Fatal("expected fetched image content to be non-empty")
	}
}

func TestOpenAIImageURLRejectsPrivateRemoteHostByDefault(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(decodeTinyPNG(t))
	}))
	defer imageServer.Close()

	provider := &fakeProvider{
		models: []gatewayruntime.Model{{
			ID:   "gpt-4.1",
			Name: "GPT-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
			Limits: gatewayruntime.ModelLimits{
				Vision: &gatewayruntime.ModelVisionLimits{
					SupportedMediaTypes: []string{"image/png"},
					MaxPromptImages:     4,
					MaxPromptImageSize:  1 << 20,
				},
			},
		}},
		nextSession: &fakeSession{
			id:       "session-image-url-private",
			response: "Image blocked",
		},
	}
	server := newTestServer(t, provider)

	body := fmt.Sprintf(`{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"text","text":"What is in this image?"},{"type":"image_url","image_url":{"url":"%s/image.png"}}]}]}`, imageServer.URL)
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected private remote image URL to be rejected, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "private or special-use remote URLs are disabled") {
		t.Fatalf("unexpected error body %s", recorder.Body.String())
	}
}

func TestValidateRemoteAttachmentURLRejectsSpecialUseIPRanges(t *testing.T) {
	for _, rawURL := range []string{
		"http://100.64.0.1/demo.png",
		"http://198.18.0.1/demo.png",
		"http://192.0.2.1/demo.png",
		"http://[2001:db8::1]/demo.png",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %q: %v", rawURL, err)
		}
		if err := validateRemoteAttachmentURL(parsed, false); err == nil {
			t.Fatalf("expected %s to be rejected", rawURL)
		}
	}
}

func TestPersistentSessionRejectsToolConfigurationMismatch(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:       "session-tools",
			response: "ok",
		},
	}
	server := newTestServer(t, provider)

	first := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"hello"}],"x_copilot":{"tools":[{"name":"lookup_issue","description":"Lookup issue","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}]}}`
	firstResponse := performRequest(server, http.MethodPost, "/v1/chat/completions", first, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "tool-session",
	})
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("expected initial streaming request to succeed, got %d: %s", firstResponse.Code, firstResponse.Body.String())
	}

	second := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"hello again"}],"x_copilot":{"tools":[{"name":"lookup_pr","description":"Lookup pull request","parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}]}}`
	secondResponse := performRequest(server, http.MethodPost, "/v1/chat/completions", second, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "tool-session",
	})
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("expected changed tool config to be rejected, got %d: %s", secondResponse.Code, secondResponse.Body.String())
	}
}

func TestUnauthorized(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"messages":[{"role":"user","content":"Hello"}]}`, nil)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestHandleModelsTimeoutReturns504(t *testing.T) {
	server := newTestServerWithConfig(t, &fakeProvider{
		modelDelay: 200 * time.Millisecond,
	}, func(cfg *config.Config) {
		cfg.RequestTimeout = 20 * time.Millisecond
	})
	recorder := performRequest(server, http.MethodGet, "/v1/models", "", map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 for model listing timeout, got %d", recorder.Code)
	}
}

func TestHandleModelsIncludesCopilotMetadata(t *testing.T) {
	server := newTestServer(t, &fakeProvider{
		models: []gatewayruntime.Model{{
			ID:   "gpt-4.1",
			Name: "GPT-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision:          true,
				ReasoningEffort: true,
			},
			Limits: gatewayruntime.ModelLimits{
				MaxContextWindowTokens: 128000,
			},
			SupportedReasoningEfforts: []string{"low", "high"},
			DefaultReasoningEffort:    "low",
		}},
	})
	recorder := performRequest(server, http.MethodGet, "/v1/models", "", map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data []struct {
			XCopilot struct {
				Supports struct {
					Vision bool `json:"vision"`
				} `json:"supports"`
				DefaultReasoningEffort string `json:"default_reasoning_effort"`
			} `json:"x_copilot"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || !response.Data[0].XCopilot.Supports.Vision || response.Data[0].XCopilot.DefaultReasoningEffort != "low" {
		t.Fatalf("unexpected x_copilot metadata: %#v", response)
	}
}

func TestOpenAIReasoningEffortFlowsToSession(t *testing.T) {
	provider := &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "gpt-4.1",
			Supports: gatewayruntime.ModelSupports{
				ReasoningEffort: true,
			},
			SupportedReasoningEfforts: []string{"low", "medium", "high"},
		}},
	}
	server := newTestServer(t, provider)

	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4.1","reasoning_effort":"high","messages":[{"role":"user","content":"Hello"}]}`, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.sessionOptions) != 1 || provider.sessionOptions[0].ReasoningEffort != "high" {
		t.Fatalf("expected reasoning effort to reach session options, got %#v", provider.sessionOptions)
	}
}

func TestOpenAIXCopilotExtensionsFlowToSessionAndResponse(t *testing.T) {
	provider := &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "gpt-4.1",
			Supports: gatewayruntime.ModelSupports{
				ReasoningEffort: true,
			},
		}},
		nextSession: &fakeSession{
			id:        "agent-session",
			response:  "done",
			reasoning: "reasoning trace",
			runtimeEvents: []gatewayruntime.Event{
				{
					Type: gatewayruntime.EventToolExecutionStart,
					Runtime: &gatewayruntime.RuntimeEvent{
						Type:     string(gatewayruntime.EventToolExecutionStart),
						ToolName: "view",
						CallID:   "tool-1",
					},
				},
			},
		},
	}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.SDKCustomAgents = []gatewayruntime.CustomAgent{{
			Name:   "reviewer",
			Prompt: "Review changes carefully.",
		}}
	})

	body := `{
		"model":"gpt-4.1",
		"messages":[
			{"role":"system","content":"You are strict."},
			{"role":"user","content":"Hello"}
		],
		"x_copilot":{
			"agent":"reviewer",
			"system_message_mode":"replace",
			"include_reasoning":true,
			"include_runtime_events":true
		}
	}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.sessionOptions) != 1 {
		t.Fatalf("expected one session option set, got %#v", provider.sessionOptions)
	}
	if provider.sessionOptions[0].Agent != "reviewer" || provider.sessionOptions[0].SystemPromptMode != "replace" {
		t.Fatalf("expected agent and replace mode to reach session options, got %#v", provider.sessionOptions[0])
	}

	var response struct {
		XCopilot struct {
			Agent             string `json:"agent"`
			SystemMessageMode string `json:"system_message_mode"`
			Reasoning         string `json:"reasoning"`
			RuntimeEvents     []struct {
				Type     string `json:"type"`
				ToolName string `json:"tool_name"`
				CallID   string `json:"call_id"`
			} `json:"runtime_events"`
		} `json:"x_copilot"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.XCopilot.Agent != "reviewer" || response.XCopilot.SystemMessageMode != "replace" || response.XCopilot.Reasoning != "reasoning trace" {
		t.Fatalf("unexpected x_copilot response %#v", response.XCopilot)
	}
	if len(response.XCopilot.RuntimeEvents) != 1 || response.XCopilot.RuntimeEvents[0].Type != string(gatewayruntime.EventToolExecutionStart) || response.XCopilot.RuntimeEvents[0].ToolName != "view" {
		t.Fatalf("unexpected x_copilot runtime events %#v", response.XCopilot.RuntimeEvents)
	}
}

func TestOpenAIStreamingIncludesCopilotRuntimeEvents(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:       "stream-agent",
			deltas:   []string{"Hel", "lo"},
			response: "Hello",
			runtimeEvents: []gatewayruntime.Event{
				{
					Type: gatewayruntime.EventReasoningDelta,
					Runtime: &gatewayruntime.RuntimeEvent{
						Type:  string(gatewayruntime.EventReasoningDelta),
						Delta: "step",
					},
				},
				{
					Type: gatewayruntime.EventToolExecutionStart,
					Runtime: &gatewayruntime.RuntimeEvent{
						Type:     string(gatewayruntime.EventToolExecutionStart),
						ToolName: "view",
					},
				},
			},
		},
	}
	server := newTestServer(t, provider)

	body := `{
		"model":"gpt-4.1",
		"stream":true,
		"messages":[{"role":"user","content":"Hello"}],
		"x_copilot":{"include_reasoning":true,"include_runtime_events":true}
	}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	output := recorder.Body.String()
	if !strings.Contains(output, `"x_copilot":{"event":{"type":"reasoning_delta","delta":"step"}}`) {
		t.Fatalf("expected reasoning delta stream event, got %s", output)
	}
	if !strings.Contains(output, `"x_copilot":{"event":{"type":"tool_execution_start"`) {
		t.Fatalf("expected tool runtime stream event, got %s", output)
	}
}

func TestClaudeJSONIncludesCopilotReasoningExtension(t *testing.T) {
	provider := &fakeProvider{
		nextSession: &fakeSession{
			id:        "claude-xcopilot",
			response:  "Claude reply",
			reasoning: "thinking",
		},
	}
	server := newTestServer(t, provider)

	body := `{
		"model":"claude-sonnet-4.5",
		"messages":[{"role":"user","content":"Hi"}],
		"x_copilot":{"include_reasoning":true}
	}`
	recorder := performRequest(server, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer test-key",
		"anthropic-version": "2023-06-01",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		XCopilot struct {
			Reasoning string `json:"reasoning"`
		} `json:"x_copilot"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.XCopilot.Reasoning != "thinking" {
		t.Fatalf("expected Claude response reasoning extension, got %#v", response.XCopilot)
	}
}

func TestHandleOpenAIRejectsUnknownCustomAgent(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{
		"model":"gpt-4.1",
		"messages":[{"role":"user","content":"Hello"}],
		"x_copilot":{"agent":"missing"}
	}`, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown agent, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestOpenAIImageInputMaterializesAttachment(t *testing.T) {
	provider := &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "gpt-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
			Limits: gatewayruntime.ModelLimits{
				Vision: &gatewayruntime.ModelVisionLimits{
					SupportedMediaTypes: []string{"image/png"},
					MaxPromptImages:     2,
					MaxPromptImageSize:  1024,
				},
			},
		}},
		nextSession: &fakeSession{id: "img-session", response: "ok"},
	}
	server := newTestServer(t, provider)

	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}]}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.createdSessions) != 1 || len(provider.createdSessions[0].attachments) != 1 || len(provider.createdSessions[0].attachments[0]) != 1 {
		t.Fatalf("expected one materialized attachment, got %#v", provider.createdSessions)
	}
	if _, err := os.Stat(provider.createdSessions[0].attachments[0][0].Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temporary attachment to be removed after request, stat err=%v", err)
	}
}

func TestImageInputRejectedForNonVisionModel(t *testing.T) {
	server := newTestServer(t, &fakeProvider{
		models: []gatewayruntime.Model{{ID: "gpt-4.1"}},
	})
	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}]}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-vision model, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestHistoricalImageInputRejectedAtHTTPBoundary(t *testing.T) {
	server := newTestServer(t, &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "gpt-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
		}},
	})
	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + tinyPNGBase64 + `"}}]},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for historical image input, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestInvalidImagePayloadRejectedAtHTTPBoundary(t *testing.T) {
	server := newTestServer(t, &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "gpt-4.1",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
			Limits: gatewayruntime.ModelLimits{
				Vision: &gatewayruntime.ModelVisionLimits{
					SupportedMediaTypes: []string{"image/png"},
					MaxPromptImages:     2,
					MaxPromptImageSize:  1024,
				},
			},
		}},
	})
	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid image payload, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestClaudeInvalidImagePayloadRejectedAtHTTPBoundary(t *testing.T) {
	server := newTestServer(t, &fakeProvider{
		models: []gatewayruntime.Model{{
			ID: "claude-sonnet-4.5",
			Supports: gatewayruntime.ModelSupports{
				Vision: true,
			},
			Limits: gatewayruntime.ModelLimits{
				Vision: &gatewayruntime.ModelVisionLimits{
					SupportedMediaTypes: []string{"image/png"},
					MaxPromptImages:     2,
					MaxPromptImageSize:  1024,
				},
			},
		}},
	})
	body := `{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}}]}]}`
	recorder := performRequest(server, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer test-key",
		"anthropic-version": "2023-06-01",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid Claude image payload, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestUnsupportedControlsReturn400(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4.1","temperature":0.2,"messages":[{"role":"user","content":"Hello"}]}`, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported controls, got %d", recorder.Code)
	}
}

func TestInvalidJSONReturns400(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"messages":`, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad json, got %d", recorder.Code)
	}
}

func TestLargeBodyReturns413(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	server.cfg.MaxBodyBytes = 8
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"messages":[{"role":"user","content":"Hello"}]}`, map[string]string{
		"Authorization": "Bearer test-key",
	})

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for large body, got %d", recorder.Code)
	}
}

func TestStreamWriteTimeoutPrefersIdleTimeout(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	server.cfg.RequestTimeout = 2 * time.Minute
	server.cfg.StreamIdleTimeout = 15 * time.Second

	if timeout := server.streamWriteTimeout(); timeout != 15*time.Second {
		t.Fatalf("expected stream idle timeout, got %s", timeout)
	}
}

func TestSetWriteDeadlineIgnoresUnsupportedWriters(t *testing.T) {
	if err := setWriteDeadline(httptest.NewRecorder(), time.Second); err != nil {
		t.Fatalf("expected unsupported write deadline to be ignored, got %v", err)
	}
}

func TestParseErrorsAreRecorded(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server.recorder = usage.NewRecorder(logger)

	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"messages":`, map[string]string{
		"Authorization": "Bearer test-key",
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad json, got %d", recorder.Code)
	}

	output := logs.String()
	if !strings.Contains(output, "route=/v1/chat/completions") || !strings.Contains(output, "status_code=400") || !strings.Contains(output, "error_type=invalid_request_error") {
		t.Fatalf("expected parse error to be recorded, got %s", output)
	}
}

func TestStreamRuntimeEventClonesMutableFields(t *testing.T) {
	success := true
	readOnly := true
	allowFreeform := true
	event := gatewayruntime.Event{
		Type: gatewayruntime.EventToolExecutionComplete,
		Runtime: &gatewayruntime.RuntimeEvent{
			Type:      string(gatewayruntime.EventToolExecutionComplete),
			Arguments: map[string]any{"nested": map[string]any{"value": "before"}},
			Success:   &success,
			Telemetry: map[string]any{"count": 1},
			PermissionRequest: &gatewayruntime.PermissionRequestSummary{
				Commands: []string{"view"},
				ReadOnly: &readOnly,
			},
			UserInputRequest: &gatewayruntime.UserInputRequestSummary{
				Choices:         []string{"a"},
				AllowFreeform:   &allowFreeform,
				RequestedSchema: map[string]any{"type": "object"},
			},
			AllowedTools: []string{"view"},
		},
	}
	prepared := &preparedConversation{includeRuntimeEvents: true}

	cloned := streamRuntimeEvent(event, prepared)
	if cloned == nil {
		t.Fatalf("expected runtime event clone")
	}

	event.Runtime.Arguments.(map[string]any)["nested"].(map[string]any)["value"] = "after"
	*event.Runtime.Success = false
	event.Runtime.Telemetry["count"] = 2
	event.Runtime.PermissionRequest.Commands[0] = "edit"
	*event.Runtime.PermissionRequest.ReadOnly = false
	event.Runtime.UserInputRequest.Choices[0] = "b"
	*event.Runtime.UserInputRequest.AllowFreeform = false
	event.Runtime.UserInputRequest.RequestedSchema["type"] = "array"
	event.Runtime.AllowedTools[0] = "edit"

	if got := cloned.Arguments.(map[string]any)["nested"].(map[string]any)["value"]; got != "before" {
		t.Fatalf("expected cloned arguments to be isolated, got %#v", got)
	}
	if cloned.Success == nil || !*cloned.Success {
		t.Fatalf("expected cloned success flag to stay true, got %#v", cloned.Success)
	}
	if got := cloned.Telemetry["count"]; got != 1 {
		t.Fatalf("expected cloned telemetry to stay 1, got %#v", got)
	}
	if got := cloned.PermissionRequest.Commands[0]; got != "view" {
		t.Fatalf("expected cloned commands to stay view, got %q", got)
	}
	if cloned.PermissionRequest.ReadOnly == nil || !*cloned.PermissionRequest.ReadOnly {
		t.Fatalf("expected cloned read_only to stay true, got %#v", cloned.PermissionRequest.ReadOnly)
	}
	if got := cloned.UserInputRequest.Choices[0]; got != "a" {
		t.Fatalf("expected cloned choices to stay a, got %q", got)
	}
	if cloned.UserInputRequest.AllowFreeform == nil || !*cloned.UserInputRequest.AllowFreeform {
		t.Fatalf("expected cloned allow_freeform to stay true, got %#v", cloned.UserInputRequest.AllowFreeform)
	}
	if got := cloned.UserInputRequest.RequestedSchema["type"]; got != "object" {
		t.Fatalf("expected cloned schema to stay object, got %#v", got)
	}
	if got := cloned.AllowedTools[0]; got != "view" {
		t.Fatalf("expected cloned allowed tools to stay view, got %q", got)
	}
}

func TestClassifyErrorTreatsContextCanceledAsCancelled(t *testing.T) {
	status, errorType, message := classifyError(context.Canceled)
	if status != http.StatusServiceUnavailable || errorType != "cancelled" || message != "request cancelled" {
		t.Fatalf("unexpected classifyError result: %d %q %q", status, errorType, message)
	}
}

func TestSessionReuseIsScopedPerAPIKey(t *testing.T) {
	provider := &fakeProvider{
		sequence: []*fakeSession{
			{id: "session-one", response: "ok"},
			{id: "session-two", response: "ok"},
		},
	}
	server := newTestServer(t, provider)

	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"}]}`
	first := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "shared-session",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", first.Code)
	}

	second := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer other-key",
		"X-Session-ID":  "shared-session",
	})
	if second.Code != http.StatusOK {
		t.Fatalf("expected second request to succeed, got %d", second.Code)
	}

	if len(provider.createdSessions) != 2 {
		t.Fatalf("expected different API keys to create isolated sessions, got %d sessions", len(provider.createdSessions))
	}
}

func TestInvalidSessionKeyReturns400(t *testing.T) {
	server := newTestServer(t, &fakeProvider{})
	recorder := performRequest(server, http.MethodPost, "/v1/chat/completions", `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"}]}`, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "bad key with spaces",
	})

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid session key, got %d", recorder.Code)
	}
}

func TestSessionLimitPerAPIKeyReturns429(t *testing.T) {
	provider := &fakeProvider{
		sequence: []*fakeSession{
			{id: "session-one", response: "ok"},
			{id: "session-two", response: "ok"},
		},
	}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.MaxSessionsPerKey = 1
		cfg.MaxActiveSessions = 4
	})

	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"}]}`
	first := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-one",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("expected first persistent session to succeed, got %d", first.Code)
	}

	second := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-two",
	})
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when per-key session limit is reached, got %d", second.Code)
	}
}

func TestGlobalSessionLimitReturns503(t *testing.T) {
	provider := &fakeProvider{
		sequence: []*fakeSession{
			{id: "session-one", response: "ok"},
			{id: "session-two", response: "ok"},
		},
	}
	server := newTestServerWithConfig(t, provider, func(cfg *config.Config) {
		cfg.MaxActiveSessions = 1
		cfg.MaxSessionsPerKey = 4
	})

	body := `{"model":"gpt-4.1","messages":[{"role":"user","content":"Hello"}]}`
	first := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer test-key",
		"X-Session-ID":  "session-one",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("expected first persistent session to succeed, got %d", first.Code)
	}

	second := performRequest(server, http.MethodPost, "/v1/chat/completions", body, map[string]string{
		"Authorization": "Bearer other-key",
		"X-Session-ID":  "session-two",
	})
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when global session limit is reached, got %d", second.Code)
	}
}

type fakeProvider struct {
	mu              sync.Mutex
	models          []gatewayruntime.Model
	modelDelay      time.Duration
	nextSession     *fakeSession
	sequence        []*fakeSession
	resumable       map[string]*fakeSession
	createdSessions []*fakeSession
	resumedSessions []string
	deletedSessions []string
	sessionOptions  []gatewayruntime.SessionOptions
}

type bridgeProvider struct {
	session        *bridgeSession
	sessionOptions []gatewayruntime.SessionOptions
}

type permissionBridgeProvider struct {
	session        *permissionBridgeSession
	sessionOptions []gatewayruntime.SessionOptions
}

func (p *bridgeProvider) Start(context.Context) error { return nil }
func (p *bridgeProvider) Close() error                { return nil }

func (p *bridgeProvider) ListModels(context.Context) ([]gatewayruntime.Model, error) {
	return []gatewayruntime.Model{{ID: "gpt-4.1", Name: "GPT-4.1"}}, nil
}

func (p *bridgeProvider) NewSession(_ context.Context, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.sessionOptions = append(p.sessionOptions, options)
	p.session.id = options.SessionID
	return p.session, nil
}

func (p *bridgeProvider) ResumeSession(_ context.Context, _ string, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.sessionOptions = append(p.sessionOptions, options)
	return p.session, nil
}

func (p *bridgeProvider) DeleteSession(context.Context, string) error { return nil }

func (p *permissionBridgeProvider) Start(context.Context) error { return nil }
func (p *permissionBridgeProvider) Close() error                { return nil }

func (p *permissionBridgeProvider) ListModels(context.Context) ([]gatewayruntime.Model, error) {
	return []gatewayruntime.Model{{ID: "gpt-4.1", Name: "GPT-4.1"}}, nil
}

func (p *permissionBridgeProvider) NewSession(_ context.Context, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.sessionOptions = append(p.sessionOptions, options)
	p.session.id = options.SessionID
	return p.session, nil
}

func (p *permissionBridgeProvider) ResumeSession(_ context.Context, _ string, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.sessionOptions = append(p.sessionOptions, options)
	return p.session, nil
}

func (p *permissionBridgeProvider) DeleteSession(context.Context, string) error { return nil }

func (p *fakeProvider) Start(context.Context) error { return nil }
func (p *fakeProvider) Close() error                { return nil }

func (p *fakeProvider) ListModels(ctx context.Context) ([]gatewayruntime.Model, error) {
	if p.modelDelay > 0 {
		select {
		case <-time.After(p.modelDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return append([]gatewayruntime.Model(nil), p.models...), nil
}

func (p *fakeProvider) NewSession(_ context.Context, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var session *fakeSession
	switch {
	case len(p.sequence) > 0:
		session = p.sequence[0]
		p.sequence = p.sequence[1:]
	case p.nextSession != nil:
		session = p.nextSession
	default:
		session = &fakeSession{id: "default-session", response: "ok"}
	}
	if options.SessionID != "" {
		session.id = options.SessionID
	}
	p.sessionOptions = append(p.sessionOptions, options)
	p.createdSessions = append(p.createdSessions, session)
	if options.SessionID != "" {
		if p.resumable == nil {
			p.resumable = make(map[string]*fakeSession)
		}
		p.resumable[options.SessionID] = session
	}
	return session, nil
}

func (p *fakeProvider) ResumeSession(_ context.Context, sessionID string, options gatewayruntime.SessionOptions) (gatewayruntime.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sessionOptions = append(p.sessionOptions, options)
	if p.resumable != nil {
		if session, ok := p.resumable[sessionID]; ok {
			p.resumedSessions = append(p.resumedSessions, sessionID)
			return session, nil
		}
	}
	return nil, gatewayruntime.ErrSessionNotFound
}

func (p *fakeProvider) DeleteSession(_ context.Context, sessionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.deletedSessions = append(p.deletedSessions, sessionID)
	if p.resumable != nil {
		delete(p.resumable, sessionID)
	}
	return nil
}

type fakeSession struct {
	id             string
	response       string
	deltas         []string
	reasoning      string
	runtimeEvents  []gatewayruntime.Event
	usage          gatewayruntime.Usage
	attachments    [][]gatewayruntime.Attachment
	attachmentData [][][]byte
	sendErr        error

	mu         sync.Mutex
	prompts    []string
	closeCount int
}

type bridgeSession struct {
	id        string
	requested chan struct{}
	responses chan gatewayruntime.PendingResponse
}

type permissionBridgeSession struct {
	id                 string
	requested          chan struct{}
	responses          chan gatewayruntime.PendingResponse
	afterResponseDelay time.Duration
}

func newBridgeSession(id string) *bridgeSession {
	return &bridgeSession{
		id:        id,
		requested: make(chan struct{}, 1),
		responses: make(chan gatewayruntime.PendingResponse, 1),
	}
}

func newPermissionBridgeSession(id string) *permissionBridgeSession {
	return &permissionBridgeSession{
		id:        id,
		requested: make(chan struct{}, 1),
		responses: make(chan gatewayruntime.PendingResponse, 1),
	}
}

func (s *bridgeSession) ID() string { return s.id }

func (s *bridgeSession) Send(ctx context.Context, _ gatewayruntime.MessageOptions, handler gatewayruntime.EventHandler) (gatewayruntime.Result, error) {
	if handler != nil {
		if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventExternalToolRequested, Runtime: &gatewayruntime.RuntimeEvent{
			Type:      string(gatewayruntime.EventExternalToolRequested),
			RequestID: "req-1",
			CallID:    "call-1",
			ToolName:  "lookup_issue",
			Arguments: map[string]any{"id": "123"},
		}}); err != nil {
			return gatewayruntime.Result{}, err
		}
	}
	select {
	case s.requested <- struct{}{}:
	default:
	}

	var response gatewayruntime.PendingResponse
	select {
	case response = <-s.responses:
	case <-ctx.Done():
		return gatewayruntime.Result{}, ctx.Err()
	}
	if response.RequestID != "req-1" || response.ToolResult == nil || response.ToolResult.TextResult != "Issue 123" {
		return gatewayruntime.Result{}, errors.New("unexpected continuation payload")
	}
	if handler != nil {
		if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventMessageDelta, Delta: "Done after tool"}); err != nil {
			return gatewayruntime.Result{}, err
		}
	}
	return gatewayruntime.Result{
		SessionID: s.id,
		Content:   "Done after tool",
	}, nil
}

func (s *bridgeSession) ResolvePending(_ context.Context, response gatewayruntime.PendingResponse) error {
	s.responses <- response
	return nil
}

func (s *bridgeSession) Close() error { return nil }

func (s *permissionBridgeSession) ID() string { return s.id }

func (s *permissionBridgeSession) Send(ctx context.Context, _ gatewayruntime.MessageOptions, handler gatewayruntime.EventHandler) (gatewayruntime.Result, error) {
	if handler != nil {
		if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventPermissionRequested, Runtime: &gatewayruntime.RuntimeEvent{
			Type:      string(gatewayruntime.EventPermissionRequested),
			RequestID: "perm-1",
			CallID:    "call-1",
			PermissionRequest: &gatewayruntime.PermissionRequestSummary{
				Kind:      "shell",
				Intention: "Run approval-gated command",
				Commands:  []string{"bash"},
			},
		}}); err != nil {
			return gatewayruntime.Result{}, err
		}
	}
	select {
	case s.requested <- struct{}{}:
	default:
	}

	var response gatewayruntime.PendingResponse
	select {
	case response = <-s.responses:
	case <-ctx.Done():
		return gatewayruntime.Result{}, ctx.Err()
	}
	if response.RequestID != "perm-1" || response.Permission == nil || response.Permission.ResultKind != "approved" {
		return gatewayruntime.Result{}, errors.New("unexpected permission continuation payload")
	}
	if s.afterResponseDelay > 0 {
		select {
		case <-time.After(s.afterResponseDelay):
		case <-ctx.Done():
			return gatewayruntime.Result{}, ctx.Err()
		}
	}
	if handler != nil {
		if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventMessageDelta, Delta: "Done after permission"}); err != nil {
			return gatewayruntime.Result{}, err
		}
	}
	return gatewayruntime.Result{
		SessionID: s.id,
		Content:   "Done after permission",
	}, nil
}

func (s *permissionBridgeSession) ResolvePending(_ context.Context, response gatewayruntime.PendingResponse) error {
	s.responses <- response
	return nil
}

func (s *permissionBridgeSession) Close() error { return nil }

func (s *fakeSession) ID() string { return s.id }

func (s *fakeSession) Send(_ context.Context, message gatewayruntime.MessageOptions, handler gatewayruntime.EventHandler) (gatewayruntime.Result, error) {
	s.mu.Lock()
	s.prompts = append(s.prompts, message.Prompt)
	s.attachments = append(s.attachments, append([]gatewayruntime.Attachment(nil), message.Attachments...))
	payloads := make([][]byte, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		data, err := os.ReadFile(attachment.Path)
		if err != nil {
			payloads = append(payloads, nil)
			continue
		}
		payloads = append(payloads, data)
	}
	s.attachmentData = append(s.attachmentData, payloads)
	s.mu.Unlock()

	if s.response == "" {
		s.response = strings.Join(s.deltas, "")
	}

	for _, delta := range s.deltas {
		if handler != nil {
			if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventMessageDelta, Delta: delta}); err != nil {
				return gatewayruntime.Result{}, err
			}
		}
	}
	for _, event := range s.runtimeEvents {
		if handler != nil {
			if err := handler(event); err != nil {
				return gatewayruntime.Result{}, err
			}
		}
	}
	if s.sendErr != nil {
		return gatewayruntime.Result{}, s.sendErr
	}
	if handler != nil {
		if err := handler(gatewayruntime.Event{Type: gatewayruntime.EventUsage, Usage: s.usage}); err != nil {
			return gatewayruntime.Result{}, err
		}
	}

	return gatewayruntime.Result{
		SessionID:     s.id,
		Content:       s.response,
		Reasoning:     s.reasoning,
		Usage:         s.usage,
		RuntimeEvents: append([]gatewayruntime.RuntimeEvent(nil), collectRuntimeEvents(s.runtimeEvents)...),
	}, nil
}

func (s *fakeSession) ResolvePending(context.Context, gatewayruntime.PendingResponse) error {
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCount++
	return nil
}

func newTestServer(t *testing.T, provider gatewayruntime.Provider) *Server {
	return newTestServerWithConfig(t, provider, nil)
}

func newTestServerWithConfig(t *testing.T, provider gatewayruntime.Provider, mutate func(*config.Config)) *Server {
	t.Helper()
	cfg := config.Config{
		ListenAddr:        ":0",
		DefaultModel:      "gpt-4.1",
		WorkingDirectory:  t.TempDir(),
		SessionStorePath:  t.TempDir() + "/sessions.json",
		SessionTTL:        5 * time.Minute,
		RequestTimeout:    5 * time.Second,
		StreamIdleTimeout: 5 * time.Second,
		MaxBodyBytes:      1 << 20,
		MaxActiveSessions: 32,
		MaxSessionsPerKey: 8,
		APIKeys: []config.APIKey{
			{Label: "test", Secret: "test-key"},
			{Label: "other", Secret: "other-key"},
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(
		cfg,
		auth.NewStore(cfg.APIKeys),
		provider,
		session.NewManagerWithStore(cfg.SessionTTL, session.Limits{
			MaxActiveSessions:     cfg.MaxActiveSessions,
			MaxActivePerNamespace: cfg.MaxSessionsPerKey,
		}, session.NewFileStore(cfg.SessionStorePath)),
		usage.NewRecorder(logger),
		logger,
	)
}

func performRequest(server *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func collectRuntimeEvents(events []gatewayruntime.Event) []gatewayruntime.RuntimeEvent {
	collected := make([]gatewayruntime.RuntimeEvent, 0, len(events))
	for _, event := range events {
		if event.Runtime != nil {
			collected = append(collected, *event.Runtime)
		}
	}
	return collected
}
