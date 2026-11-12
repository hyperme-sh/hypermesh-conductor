// Optional, single-tenant lease accounting for a Conductor instance.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
)

type planeReservation struct {
	Task   string `json:"task"`
	Step   string `json:"step"`
	Model  string `json:"model"`
	Recipe string `json:"recipe"`
	Source string `json:"input_source"`
	Input  int    `json:"input_tokens"`
	Output int    `json:"output_tokens"`
}

func registerReservation(ctx context.Context, endpoint, tenant, lease, token, attempt string, hold planeReservation, transport http.RoundTripper) error {
	base, err := url.Parse(endpoint)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("invalid plane accounting endpoint")
	}
	if _, err := uuid.Parse(tenant); err != nil {
		return errors.New("invalid plane accounting tenant")
	}
	if _, err := uuid.Parse(lease); err != nil {
		return errors.New("invalid plane accounting lease")
	}
	if _, err := uuid.Parse(attempt); err != nil {
		return errors.New("invalid plane accounting attempt")
	}
	if token == "" {
		return errors.New("missing plane accounting credential")
	}
	body, err := json.Marshal(hold)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", base.JoinPath("api/v1/hypermesh/leases", lease, "token-attempts", attempt).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Tenant-ID", tenant)
	client := http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("plane reservation status %d", resp.StatusCode)
	}
	var ack struct {
		State    string            `json:"state"`
		Reserved *planeReservation `json:"reserved"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&ack); err != nil || ack.State != "reserved" || ack.Reserved == nil || *ack.Reserved != hold {
		return errors.New("plane did not confirm the requested hold")
	}
	return nil
}

func registerConfiguredReservation(ctx context.Context, endpoint, attempt string, hold planeReservation) error {
	return registerReservation(ctx, endpoint, os.Getenv("CONDUCTOR_ACCOUNTING_TENANT"), os.Getenv("CONDUCTOR_ACCOUNTING_LEASE"),
		strings.TrimSpace(os.Getenv("CONDUCTOR_ACCOUNTING_TOKEN")), attempt, hold, nil)
}
