package proxy

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestPlaneScopeAndAmbiguousAcknowledgements(t *testing.T) {
	tenant, lease, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	hold := planeReservation{Task: "task", Step: "step", Model: "catalog-model", Recipe: "revision", Source: "vllm", Input: 11, Output: 22}
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer server.Close()
	for _, scope := range []struct{ tenant, lease, attempt string }{{"bad", lease, attempt}, {tenant, "bad", attempt}, {tenant, lease, "../../other"}} {
		if err := registerReservation(context.Background(), server.URL, scope.tenant, scope.lease, "lease-token", scope.attempt, hold, server.Client().Transport); err == nil {
			t.Fatal("invalid scope accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid scope dispatched")
	}
	if err := registerReservation(context.Background(), server.URL, tenant, lease, "lease-token", attempt, hold, server.Client().Transport); err == nil {
		t.Fatal("ambiguous response accepted")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestPlaneModelRevisionIsOptionalAndBound(t *testing.T) {
	hold := planeReservation{Task: "task", Step: "step", Model: "catalog", Recipe: "revision", Source: "vllm", Input: 3, Output: 4}
	legacy, err := json.Marshal(hold)
	if err != nil || strings.Contains(string(legacy), "model_revision") {
		t.Fatal("legacy shape changed", err)
	}
	hold.ModelRevision = "sha256:original"
	for _, revision := range []string{"sha256:original", "sha256:other", ""} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var got planeReservation
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got.ModelRevision != hold.ModelRevision {
				t.Error("revision missing from request")
			}
			got.ModelRevision = revision
			json.NewEncoder(w).Encode(map[string]any{"state": "reserved", "reserved": got})
		}))
		err := registerReservation(context.Background(), server.URL, uuid.NewString(), uuid.NewString(), "key", uuid.NewString(), hold, server.Client().Transport)
		server.Close()
		if (err == nil) != (revision == hold.ModelRevision) {
			t.Fatal(revision, err)
		}
	}
}
