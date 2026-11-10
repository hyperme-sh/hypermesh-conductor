// Package leases prepares ticketed tenant capacity using the installed Orchestrator client.
package leases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

type Lease struct {
	DeviceID   string `json:"device_id"`
	LeaseID    string `json:"lease_id"`
	OpenedHere bool   `json:"opened_here"`
}

type Result struct {
	SchemaVersion int     `json:"schema_version"`
	Leases        []Lease `json:"leases"`
}

// Prepare never sends a prompt. On success the caller owns leases marked OpenedHere.
// The trusted helper reads the renter credential from its inherited environment.
func Prepare(ctx context.Context, tenant, catalog, apiBase string) (Result, error) {
	var result Result
	if tenant == "" {
		return result, errors.New("tenant is required")
	}
	args := []string{"--prepare-leases", "--tenant", tenant}
	if catalog != "" {
		args = append(args, "--catalog-id", catalog)
	}
	if apiBase != "" {
		args = append(args, "--api-base", apiBase)
	}
	payload, err := exec.CommandContext(ctx, "hypermesh-orchestrator", args...).Output()
	if err != nil {
		return result, fmt.Errorf("orchestrator lease preparation: %w", err)
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return Result{}, fmt.Errorf("invalid lease response: %w", err)
	}
	if result.SchemaVersion != 1 || len(result.Leases) == 0 {
		return Result{}, errors.New("unsupported or empty lease response")
	}
	for _, lease := range result.Leases {
		if lease.LeaseID == "" || lease.DeviceID == "" {
			return Result{}, errors.New("incomplete lease response")
		}
	}
	return result, nil
}
