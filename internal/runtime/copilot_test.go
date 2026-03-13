package runtime

import (
	"testing"

	copilot "github.com/github/copilot-sdk/go"
)

func TestStatusCodeForAuthorizationError(t *testing.T) {
	if got := statusCodeForErrorType("authorization"); got != 403 {
		t.Fatalf("expected 403 for authorization error, got %d", got)
	}
}

func TestPermissionHandlerForMode(t *testing.T) {
	allowed, err := permissionHandlerForMode(PermissionModeAllow)(copilot.PermissionRequest{}, copilot.PermissionInvocation{})
	if err != nil {
		t.Fatalf("allow handler returned error: %v", err)
	}
	if allowed.Kind != copilot.PermissionRequestResultKindApproved {
		t.Fatalf("expected allow mode to approve, got %#v", allowed)
	}

	denied, err := permissionHandlerForMode(PermissionModeDeny)(copilot.PermissionRequest{}, copilot.PermissionInvocation{})
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
