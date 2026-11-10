package proxy

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlaneRequiresMatchingUnfinalizedHold(t *testing.T) {
	tenant, lease, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	hold := planeReservation{Task: "task", Step: "step", Input: 11, Output: 22}
	for _, state := range []string{"reserved", "final", "mismatch"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "PUT" || r.URL.Path != "/api/v1/hypermesh/leases/"+lease+"/token-attempts/"+attempt || r.Header.Get("X-Tenant-ID") != tenant || r.Header.Get("Authorization") != "Bearer lease-token" {
					t.Error("wrong registration request")
				}
				var got planeReservation
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != hold {
					t.Error("wrong hold")
				}
				status := state
				if state == "mismatch" {
					status = "reserved"
					got.Output++
				}
				json.NewEncoder(w).Encode(map[string]any{"state": status, "reserved": got})
			}))
			defer server.Close()
			err := registerReservation(context.Background(), server.URL, tenant, lease, "lease-token", attempt, hold, server.Client().Transport)
			if (err == nil) != (state == "reserved") {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}
