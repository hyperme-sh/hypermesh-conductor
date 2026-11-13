// Package capabilityreview carries enrolled-host calls to AIuditor. It never
// holds private machine keys or supplies a local allow fallback.
package capabilityreview

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/google/uuid"
)

var errRefused = errors.New("capability review refused")
var hash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var identity = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var name = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var scope = regexp.MustCompile(`^org/[A-Za-z0-9_-]+/site/[A-Za-z0-9_-]+$`)

// Client requires an authenticated HTTPS authority. HTTP is not accepted by
// this client, even when a caller claims that it is inside a tunnel.
type Client struct {
	Endpoint string
	HTTP     *http.Client
	Now      func() time.Time
}

type Decision struct {
	Protocol      int    `json:"protocol_version"`
	CallID        string `json:"call_id"`
	RequestSHA256 string `json:"request_sha256"`
	Decision      string `json:"decision"`
	Expires       int64  `json:"expires_at"`
}

// object accepts one flat object, rejects duplicate keys, and preserves numbers
// so the signed integer timestamps are never rounded through floating point.
func object(raw []byte, max int) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > max {
		return nil, errRefused
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errRefused
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errRefused
		}
		if _, exists := result[key]; exists {
			return nil, errRefused
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errRefused
		}
		result[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return nil, errRefused
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errRefused
	}
	return result, nil
}

func signed(raw []byte, now int64) (map[string]json.RawMessage, string, int64, string, error) {
	fields, err := object(raw, 16384)
	if err != nil || len(fields) != 20 {
		return nil, "", 0, "", errRefused
	}
	stringsMap := map[string]string{}
	for key, value := range fields {
		switch key {
		case "protocol_version", "issued_at", "expires_at":
		default:
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, "", 0, "", errRefused
			}
			stringsMap[key] = text
		}
	}
	var protocol int
	var issued, expires int64
	if json.Unmarshal(fields["protocol_version"], &protocol) != nil || protocol != 1 || json.Unmarshal(fields["issued_at"], &issued) != nil || json.Unmarshal(fields["expires_at"], &expires) != nil {
		return nil, "", 0, "", errRefused
	}
	if issued < now-30 || issued > now+5 || expires <= now || expires > issued+30 {
		return nil, "", 0, "", errRefused
	}
	for _, key := range []string{"authority_id", "control_plane_id"} {
		if !identity.MatchString(stringsMap[key]) {
			return nil, "", 0, "", errRefused
		}
	}
	for _, key := range []string{"tenant_id", "device_id", "request_id", "call_id"} {
		text := stringsMap[key]
		parsed, err := uuid.Parse(text)
		if err != nil || parsed.String() != text {
			return nil, "", 0, "", errRefused
		}
	}
	for _, key := range []string{"source_sha256", "graph_sha256", "arguments_sha256", "nonce"} {
		if !hash.MatchString(stringsMap[key]) {
			return nil, "", 0, "", errRefused
		}
	}
	for _, key := range []string{"owner", "capability_id", "revision"} {
		text := stringsMap[key]
		if !name.MatchString(text) || bytes.Contains([]byte(text), []byte("..")) || bytes.Contains([]byte(text), []byte("//")) || text[len(text)-1] == '/' {
			return nil, "", 0, "", errRefused
		}
	}
	if text := stringsMap["scope"]; len(text) > 270 || !scope.MatchString(text) {
		return nil, "", 0, "", errRefused
	}
	public, err := base64.StdEncoding.DecodeString(stringsMap["public_key"])
	if err != nil || len(public) != 32 || base64.StdEncoding.EncodeToString(public) != stringsMap["public_key"] {
		return nil, "", 0, "", errRefused
	}
	wg, err := base64.StdEncoding.DecodeString(stringsMap["wireguard_public_key"])
	if err != nil || len(wg) != 32 || bytes.Equal(wg, make([]byte, 32)) || base64.StdEncoding.EncodeToString(wg) != stringsMap["wireguard_public_key"] {
		return nil, "", 0, "", errRefused
	}
	signature, err := base64.StdEncoding.DecodeString(stringsMap["signature"])
	if err != nil || len(signature) != 64 || base64.StdEncoding.EncodeToString(signature) != stringsMap["signature"] {
		return nil, "", 0, "", errRefused
	}
	// Re-encode typed values, matching the authority's compact sorted-key format.
	canonical := map[string]any{}
	for key, value := range stringsMap {
		if key != "signature" {
			canonical[key] = value
		}
	}
	canonical["protocol_version"] = protocol
	canonical["issued_at"] = issued
	canonical["expires_at"] = expires
	encoded, _ := json.Marshal(canonical)
	message := append([]byte("hypermesh-capability-review-v1\n"), encoded...)
	if !ed25519.Verify(public, message, signature) {
		return nil, "", 0, "", errRefused
	}
	digest := sha256.Sum256(message)
	return fields, stringsMap["call_id"], expires, hex.EncodeToString(digest[:]), nil
}

func (c Client) Review(ctx context.Context, raw []byte) (Decision, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	_, call, expires, digest, err := signed(raw, now().Unix())
	if err != nil {
		return Decision{}, errRefused
	}
	target, err := url.Parse(c.Endpoint)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Path != "/api/v1/aiuditor/capabilities/review" {
		return Decision{}, errRefused
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(raw))
	if err != nil {
		return Decision{}, errRefused
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return Decision{}, errRefused
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Decision{}, errRefused
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return Decision{}, errRefused
	}
	fields, err := object(body, 4096)
	if err != nil || len(fields) != 5 {
		return Decision{}, errRefused
	}
	var result Decision
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || result.Protocol != 1 || result.CallID != call || result.RequestSHA256 != digest || result.Expires != expires || now().Unix() >= expires || (result.Decision != "allow" && result.Decision != "deny") {
		return Decision{}, errRefused
	}
	return result, nil
}
