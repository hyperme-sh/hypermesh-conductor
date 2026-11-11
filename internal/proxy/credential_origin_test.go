package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"net/http/httptest"
	"testing"
)

func TestDirectCredentialOrigin(t *testing.T) {
	tier := config.Tier{Direct: true, BaseURL: "https://provider.example/v1", Provider: config.ProviderOpenAI, Auth: config.AuthPassthrough, CredentialOrigin: "https://provider.example"}
	for _, origin := range []string{"", "https://gateway.example", "https://provider.example"} {
		src := httptest.NewRequest("POST", "http://localhost/v1/chat/completions", nil)
		src.Header.Set("Authorization", "Bearer provider-only")
		if origin != "" {
			src.Header.Set("X-Conductor-Credential-Origin", origin)
		}
		dst := httptest.NewRequest("POST", tier.BaseURL+"/chat/completions", nil)
		_, err := applyAuth(dst, src, tier)
		if origin == tier.CredentialOrigin {
			if err != nil || dst.Header.Get("Authorization") != "Bearer provider-only" {
				t.Fatal(err)
			}
		} else {
			if err == nil || dst.Header.Get("Authorization") != "" {
				t.Fatal("credential escaped origin")
			}
		}
	}
}
