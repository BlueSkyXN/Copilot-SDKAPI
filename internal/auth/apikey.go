package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"copilot-sdkapi/internal/config"
)

var ErrUnauthorized = errors.New("unauthorized")

type Identity struct {
	Label            string
	SessionNamespace string
}

type Store struct {
	keys []storedKey
}

type storedKey struct {
	secret   string
	identity Identity
}

func NewStore(keys []config.APIKey) *Store {
	entries := make([]storedKey, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, storedKey{
			secret: key.Secret,
			identity: Identity{
				Label:            key.Label,
				SessionNamespace: namespaceForSecret(key.Secret),
			},
		})
	}
	return &Store{keys: entries}
}

func (s *Store) AuthenticateRequest(r *http.Request) (Identity, error) {
	token := extractToken(r)
	if token == "" {
		return Identity{}, ErrUnauthorized
	}
	for _, key := range s.keys {
		if subtle.ConstantTimeCompare([]byte(key.secret), []byte(token)) == 1 {
			return key.identity, nil
		}
	}
	return Identity{}, ErrUnauthorized
}

func extractToken(r *http.Request) string {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization != "" {
		if token, found := strings.CutPrefix(authorization, "Bearer "); found {
			return strings.TrimSpace(token)
		}
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

func namespaceForSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}
