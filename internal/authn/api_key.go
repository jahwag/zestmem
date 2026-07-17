package authn

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
)

const (
	APIKeyPrefix = "am_"
	apiKeyBytes  = 32
)

// GenerateAPIKey creates the only opaque credential format accepted by the
// authenticator. Raw URL encoding is deliberately dot-free.
func GenerateAPIKey() (string, error) {
	raw := make([]byte, apiKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return APIKeyPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func isAPIKey(token string) bool {
	if !strings.HasPrefix(token, APIKeyPrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, APIKeyPrefix))
	return err == nil && len(raw) == apiKeyBytes
}
