package leases

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

// Run with the Orchestrator wheel installed on PATH to qualify both owners together.
func TestInstalledOrchestrator(t *testing.T) {
	if _, err := exec.LookPath("hypermesh-orchestrator"); err != nil {
		if os.Getenv("REQUIRE_LEASE_CLIENT") == "1" {
			t.Fatal(err)
		}
		t.Skip("Orchestrator client is not installed")
	}
	t.Setenv("HYPERMESH_API_KEY", "fixture")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("X-Tenant-ID") != "tenant" {
			http.Error(w, "invalid auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/aiuditor/allowed":
			fmt.Fprint(w, `{"hosts":[{"connection_id":"connection","device_id":"device"}]}`)
		case "/api/v1/hypermesh/renter/host-tickets":
			fmt.Fprint(w, `{"hosts":[{"ticket_present":true,"lease_id":"lease","device_id":"device"}]}`)
		case "/api/v1/hypermesh/leases":
			if r.Method != "GET" {
				t.Errorf("unexpected booking")
				http.Error(w, "unexpected", 400)
				return
			}
			fmt.Fprint(w, "[]")
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	got, err := Prepare(context.Background(), "tenant", "catalog", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Leases) != 1 || got.Leases[0] != (Lease{DeviceID: "device", LeaseID: "lease"}) {
		t.Fatalf("unexpected result %+v", got)
	}
}
