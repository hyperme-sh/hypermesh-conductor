package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// The operator selects the origin and allowed job classes. A request carries
// only one user message; local history, prefix, tools and credentials stay local.
type submissionRoute struct {
	Origin   string   `json:"origin"`
	Classes  []string `json:"allowed_classes"`
	TokenEnv string   `json:"token_env"`
}

func submitCmd(args []string, in io.Reader, out, stderr io.Writer) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("routes", "", "operator routes JSON")
	dest := fs.String("destination", "", "named destination")
	class := fs.String("class", "", "allowed job class")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *path == "" || *dest == "" || *class == "" {
		fmt.Fprintln(stderr, "routes, destination and class are required")
		return 2
	}
	file, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer file.Close()
	var routes map[string]submissionRoute
	dec := json.NewDecoder(file)
	dec.DisallowUnknownFields()
	if dec.Decode(&routes) != nil || dec.Decode(new(any)) != io.EOF {
		fmt.Fprintln(stderr, "invalid submission routes")
		return 2
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	err = submit(context.Background(), transport, routes[*dest], *dest, *class, in, out)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func submit(ctx context.Context, transport http.RoundTripper, route submissionRoute, destination, class string, in io.Reader, out io.Writer) error {
	if class == "" || !slices.Contains(route.Classes, class) {
		return errors.New("destination or prompt class denied")
	}
	origin, err := url.Parse(route.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return errors.New("expected a configured HTTPS origin")
	}
	token := strings.TrimSpace(os.Getenv(route.TokenEnv))
	if token == "" {
		return errors.New("destination caller credential unavailable")
	}
	const maxPrompt = 64 << 10
	text, err := io.ReadAll(io.LimitReader(in, maxPrompt+1))
	if err != nil || len(bytes.TrimSpace(text)) == 0 || len(text) > maxPrompt {
		return errors.New("expected a nonempty prompt of at most 64 KiB")
	}
	payload, err := json.Marshal(map[string]string{"destination": destination, "class": class, "prompt": string(text)})
	if err != nil {
		return err
	}
	origin.Path = "/v1/peer/prompt-submit"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Transport: transport, Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("destination request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("destination returned HTTP %d", resp.StatusCode)
	}
	const maxReply = 4 << 20
	reply, err := io.ReadAll(io.LimitReader(resp.Body, maxReply+1))
	if err != nil || len(reply) > maxReply {
		return errors.New("invalid destination response size")
	}
	_, err = out.Write(reply)
	return err
}
