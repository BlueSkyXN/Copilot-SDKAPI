package auth

import (
	"net/http/httptest"
	"testing"

	"copilot-sdkapi/internal/config"
)

func TestAuthenticateRequestSupportsBearerAndAPIKey(t *testing.T) {
	store := NewStore([]config.APIKey{
		{Label: "one", Secret: "key-one"},
		{Label: "two", Secret: "key-two"},
	})

	bearerReq := httptest.NewRequest("GET", "/", nil)
	bearerReq.Header.Set("Authorization", "Bearer key-one")
	identity, err := store.AuthenticateRequest(bearerReq)
	if err != nil {
		t.Fatalf("expected bearer auth to succeed: %v", err)
	}
	if identity.Label != "one" {
		t.Fatalf("expected label one, got %q", identity.Label)
	}

	apiKeyReq := httptest.NewRequest("GET", "/", nil)
	apiKeyReq.Header.Set("X-API-Key", "key-two")
	identity, err = store.AuthenticateRequest(apiKeyReq)
	if err != nil {
		t.Fatalf("expected x-api-key auth to succeed: %v", err)
	}
	if identity.Label != "two" {
		t.Fatalf("expected label two, got %q", identity.Label)
	}
}

func TestNamespaceForSecretProducesIsolation(t *testing.T) {
	store := NewStore([]config.APIKey{
		{Label: "same", Secret: "alpha"},
		{Label: "same", Secret: "beta"},
	})

	req1 := httptest.NewRequest("GET", "/", nil)
	req1.Header.Set("Authorization", "Bearer alpha")
	id1, err := store.AuthenticateRequest(req1)
	if err != nil {
		t.Fatalf("authenticate alpha: %v", err)
	}

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer beta")
	id2, err := store.AuthenticateRequest(req2)
	if err != nil {
		t.Fatalf("authenticate beta: %v", err)
	}

	if id1.SessionNamespace == id2.SessionNamespace {
		t.Fatalf("expected different secrets to get different session namespaces")
	}
}
