package capabilityreview

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const stamp int64 = 2000000000

func fixture(t *testing.T) []byte {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	body := map[string]any{
		"protocol_version": 1, "authority_id": "local", "control_plane_id": "plane-a",
		"tenant_id": "11111111-1111-4111-8111-111111111111", "device_id": "22222222-2222-4222-8222-222222222222",
		"request_id": "33333333-3333-4333-8333-333333333333", "call_id": "44444444-4444-4444-8444-444444444444",
		"wireguard_public_key": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		"scope":                "org/a/site/b", "owner": "owner", "capability_id": "sensor/read", "revision": "v1",
		"source_sha256": strings.Repeat("a", 64), "graph_sha256": strings.Repeat("b", 64), "arguments_sha256": strings.Repeat("c", 64),
		"nonce": strings.Repeat("d", 64), "issued_at": stamp, "expires_at": stamp + 20,
		"public_key": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
	}
	message, _ := json.Marshal(body)
	body["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte("hypermesh-capability-review-v1\n"), message...)))
	raw, _ := json.Marshal(body)
	return raw
}

func TestReviewExactResponse(t *testing.T) {
	raw := fixture(t)
	_, call, expires, digest, err := signed(raw, stamp)
	if err != nil {
		t.Fatal(err)
	}
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/aiuditor/capabilities/review" || r.Header.Get("Authorization") != "" {
					t.Error("wrong request")
				}
				json.NewEncoder(w).Encode(Decision{1, call, digest, decision, expires})
			}))
			defer server.Close()
			result, err := (Client{Endpoint: server.URL + "/api/v1/aiuditor/capabilities/review", HTTP: server.Client(), Now: func() time.Time { return time.Unix(stamp, 0) }}).Review(context.Background(), raw)
			if err != nil || result.Decision != decision {
				t.Fatal(result, err)
			}
		})
	}
}

func TestRefuseInvalidResponses(t *testing.T) {
	raw := fixture(t)
	_, call, expires, digest, _ := signed(raw, stamp)
	good, _ := json.Marshal(Decision{1, call, digest, "allow", expires})
	for label, reply := range map[string]string{
		"wrong call":       strings.Replace(string(good), call, "55555555-5555-4555-8555-555555555555", 1),
		"wrong digest":     strings.Replace(string(good), digest, strings.Repeat("0", 64), 1),
		"extended expiry":  strings.Replace(string(good), "2000000020", "2000000021", 1),
		"unknown decision": strings.Replace(string(good), "allow", "modify", 1),
		"duplicate":        strings.Replace(string(good), `"decision":"allow"`, `"decision":"deny","decision":"allow"`, 1),
		"oversized":        strings.Repeat(" ", 4097), "trailing": string(good) + "{}", "unknown field": strings.TrimSuffix(string(good), "}") + `,"extra":true}`,
	} {
		t.Run(label, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(reply)) }))
			defer server.Close()
			_, err := (Client{Endpoint: server.URL + "/api/v1/aiuditor/capabilities/review", HTTP: server.Client(), Now: func() time.Time { return time.Unix(stamp, 0) }}).Review(context.Background(), raw)
			if err == nil {
				t.Fatal("accepted invalid response")
			}
		})
	}
}

func TestRefuseTamperingBeforeNetwork(t *testing.T) {
	raw := fixture(t)
	for label, request := range map[string][]byte{
		"tampered":  bytes.Replace(raw, []byte("sensor/read"), []byte("sensor/write"), 1),
		"duplicate": bytes.Replace(raw, []byte(`"protocol_version":1`), []byte(`"protocol_version":1,"protocol_version":1`), 1),
		"float":     bytes.Replace(raw, []byte(`"protocol_version":1`), []byte(`"protocol_version":1.0`), 1),
		"trailing":  append(append([]byte{}, raw...), []byte("{}")...),
	} {
		t.Run(label, func(t *testing.T) {
			if _, _, _, _, err := signed(request, stamp); err == nil {
				t.Fatal("invalid signature accepted")
			}
		})
	}
	for _, now := range []int64{stamp - 6, stamp + 20, stamp + 31} {
		if _, _, _, _, err := signed(raw, now); err == nil {
			t.Fatal("invalid time accepted")
		}
	}
}

func TestRedirectAndExpiryDuringRequest(t *testing.T) {
	raw := fixture(t)
	_, call, expires, digest, _ := signed(raw, stamp)
	calls := 0
	now := stamp
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/aiuditor/capabilities/review" {
			t.Error("redirect followed")
		}
		if calls == 1 {
			http.Redirect(w, r, "/redirect", 307)
			return
		}
		now = expires
		json.NewEncoder(w).Encode(Decision{1, call, digest, "allow", expires})
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL + "/api/v1/aiuditor/capabilities/review", HTTP: server.Client(), Now: func() time.Time { return time.Unix(now, 0) }}
	if _, err := client.Review(context.Background(), raw); err == nil || calls != 1 {
		t.Fatal("redirect accepted")
	}
	if _, err := client.Review(context.Background(), raw); err == nil {
		t.Fatal("expired during request")
	}
	for _, endpoint := range []string{"http://localhost/api/v1/aiuditor/capabilities/review", server.URL + "/other", server.URL + "/api/v1/aiuditor/capabilities/review?token=secret"} {
		now = stamp
		client.Endpoint = endpoint
		if _, err := client.Review(context.Background(), raw); err == nil {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
