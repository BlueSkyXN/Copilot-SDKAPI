package runtime

import (
	"context"
	"errors"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestStatusCodeForAuthorizationError(t *testing.T) {
	if got := statusCodeForErrorType("authorization"); got != 403 {
		t.Fatalf("expected 403 for authorization error, got %d", got)
	}
}

func TestPermissionHandlerForMode(t *testing.T) {
	allowed, err := permissionHandlerForMode(PermissionModeAllow, newInteractiveBridge())(copilot.PermissionRequest{}, copilot.PermissionInvocation{})
	if err != nil {
		t.Fatalf("allow handler returned error: %v", err)
	}
	if allowed.Kind != copilot.PermissionRequestResultKindApproved {
		t.Fatalf("expected allow mode to approve, got %#v", allowed)
	}

	denied, err := permissionHandlerForMode(PermissionModeDeny, newInteractiveBridge())(copilot.PermissionRequest{}, copilot.PermissionInvocation{})
	if err != nil {
		t.Fatalf("deny handler returned error: %v", err)
	}
	if denied.Kind != copilot.PermissionRequestResultKindDeniedByRules {
		t.Fatalf("expected deny mode to deny by rules, got %#v", denied)
	}
}

func TestResultDetailedContentDoesNotFallbackToSummary(t *testing.T) {
	summary := "summary"
	result := &copilot.Result{Content: &summary}
	if got := resultDetailedContent(result); got != "" {
		t.Fatalf("expected empty detailed content without SDK detailed payload, got %q", got)
	}

	detailed := "expanded"
	result.DetailedContent = &detailed
	if got := resultDetailedContent(result); got != "expanded" {
		t.Fatalf("expected SDK detailed content, got %q", got)
	}
}

func TestWrapSessionLookupErrorOnlyWrapsSessionNotFoundSignals(t *testing.T) {
	sessionErr := wrapSessionLookupError(errors.New("failed to resume session: unknown session abc"))
	if !errors.Is(sessionErr, ErrSessionNotFound) {
		t.Fatalf("expected unknown session to map to ErrSessionNotFound, got %v", sessionErr)
	}

	otherErr := wrapSessionLookupError(errors.New("failed to resume session: agent not found"))
	if errors.Is(otherErr, ErrSessionNotFound) {
		t.Fatalf("expected non-session not-found error to remain untouched, got %v", otherErr)
	}
}

func TestInteractiveBridgeWaitForRequestHonorsContextCancellation(t *testing.T) {
	bridge := newInteractiveBridge()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := bridge.waitForToolRequest(cancelledCtx, "tool-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waitForToolRequest to honor context cancellation, got %v", err)
	}
	if _, err := bridge.waitForUserInputRequest(cancelledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waitForUserInputRequest to honor context cancellation, got %v", err)
	}
	if _, err := bridge.waitForPermissionRequest(cancelledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waitForPermissionRequest to honor context cancellation, got %v", err)
	}
}

func TestInteractiveBridgeRejectsConcurrentUserInputRequests(t *testing.T) {
	bridge := newInteractiveBridge()
	allowFreeform := true
	bridge.registerUserInputRequest(copilot.Data{
		RequestID:     ptrTo("user-1"),
		Question:      ptrTo("first"),
		AllowFreeform: &allowFreeform,
	})
	bridge.registerUserInputRequest(copilot.Data{
		RequestID:     ptrTo("user-2"),
		Question:      ptrTo("second"),
		AllowFreeform: &allowFreeform,
	})

	first, err := bridge.waitForUserInputRequest(context.Background())
	if err != nil {
		t.Fatalf("wait for first user input request: %v", err)
	}
	if first.request.ID != "user-1" {
		t.Fatalf("expected first request, got %q", first.request.ID)
	}

	second, err := bridge.waitForUserInputRequest(context.Background())
	if err != nil {
		t.Fatalf("wait for second user input request: %v", err)
	}
	if second.request.ID != "user-2" {
		t.Fatalf("expected second request, got %q", second.request.ID)
	}

	result := <-second.responseCh
	if !errors.Is(result.err, errConcurrentUserInputRequestsUnsupported) {
		t.Fatalf("expected concurrent user input error, got %v", result.err)
	}
}

func TestInteractiveBridgeRejectsConcurrentPermissionRequests(t *testing.T) {
	bridge := newInteractiveBridge()
	bridge.registerPermissionRequest(copilot.Data{
		RequestID: ptrTo("perm-1"),
		PermissionRequest: &copilot.PermissionRequest{
			Kind:      copilot.KindShell,
			Intention: ptrTo("first"),
		},
	})
	bridge.registerPermissionRequest(copilot.Data{
		RequestID: ptrTo("perm-2"),
		PermissionRequest: &copilot.PermissionRequest{
			Kind:      copilot.KindShell,
			Intention: ptrTo("second"),
		},
	})

	first, err := bridge.waitForPermissionRequest(context.Background())
	if err != nil {
		t.Fatalf("wait for first permission request: %v", err)
	}
	if first.request.ID != "perm-1" {
		t.Fatalf("expected first request, got %q", first.request.ID)
	}

	second, err := bridge.waitForPermissionRequest(context.Background())
	if err != nil {
		t.Fatalf("wait for second permission request: %v", err)
	}
	if second.request.ID != "perm-2" {
		t.Fatalf("expected second request, got %q", second.request.ID)
	}

	result := <-second.responseCh
	if !errors.Is(result.err, errConcurrentPermissionRequestsUnsupported) {
		t.Fatalf("expected concurrent permission error, got %v", result.err)
	}
}

func ptrTo[T any](value T) *T {
	return &value
}
