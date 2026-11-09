package cli

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
)

type peerGrant struct {
	Destination string   `json:"destination"`
	Classes     []string `json:"allowed_classes"`
	TokenEnv    string   `json:"token_env"`
}

func loadPeerIngress(path string, dispatch http.Handler) (http.Handler, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var grant peerGrant
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if dec.Decode(&grant) != nil || dec.Decode(new(any)) != io.EOF || grant.Destination == "" || len(grant.Classes) == 0 || grant.TokenEnv == "" || grant.TokenEnv == config.ProxyTokenEnv || strings.TrimSpace(os.Getenv(grant.TokenEnv)) == "" {
		return nil, errors.New("invalid peer ingress grant")
	}
	return grant.handler(dispatch), nil
}

func (grant peerGrant) handler(dispatch http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(os.Getenv(grant.TokenEnv))
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "peer grant denied", http.StatusForbidden)
			return
		}
		var envelope struct {
			Destination string
			Class       string
			Prompt      string
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(&envelope) != nil || dec.Decode(new(any)) != io.EOF || strings.TrimSpace(envelope.Prompt) == "" || len(envelope.Prompt) > 64<<10 {
			http.Error(w, "invalid prompt submission", http.StatusBadRequest)
			return
		}
		if envelope.Destination != grant.Destination || envelope.Class == "" || !slices.Contains(grant.Classes, envelope.Class) {
			http.Error(w, "peer grant denied", http.StatusForbidden)
			return
		}
		body, _ := json.Marshal(map[string]any{"messages": []map[string]string{{"role": "user", "content": envelope.Prompt}}})
		local, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://localhost/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			http.Error(w, "invalid submission", http.StatusBadRequest)
			return
		}
		local.Header.Set("Content-Type", "application/json")
		local.Header.Set("X-Conductor-Class", envelope.Class)
		local.Header.Set(config.ProxyTokenHeader, os.Getenv(config.ProxyTokenEnv))
		dispatch.ServeHTTP(w, local)
	})
}
