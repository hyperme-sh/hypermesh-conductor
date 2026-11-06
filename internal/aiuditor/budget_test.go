package aiuditor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type budgetTransport func(*http.Request) (*http.Response, error)

func (f budgetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type trackedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackedBody) Close() error { b.closed = true; return nil }
func TestHTTPResponseReservesBeforeBufferingAndReleasesOnFailure(t *testing.T) {
	body := &trackedBody{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	held := 0
	ctx := WithResponseBudget(context.Background(), func(n int) bool {
		if held+n > 1024 {
			return false
		}
		held += n
		return true
	}, func(n int) { held -= n })
	hop := HTTP{URL: "http://review.test", Client: &http.Client{Transport: budgetTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	})}}
	_, err := hop.Review(ctx, Envelope{})
	if !errors.Is(err, ErrResponseBudget) || held != 0 || body.read > 32768 || !body.closed {
		t.Fatalf("err=%v held=%d read=%d closed=%v", err, held, body.read, body.closed)
	}
}
func TestHTTPVerdictKeepsReservationUntilReleased(t *testing.T) {
	for _, raw := range []string{`{"action":"modify","body":{"id":42}}`, `not-json`} {
		held := 0
		ctx := WithResponseBudget(context.Background(), func(n int) bool { held += n; return true }, func(n int) { held -= n })
		hop := HTTP{URL: "http://review.test", Client: &http.Client{Transport: budgetTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw)), Header: http.Header{}}, nil
		})}}
		verdict, err := hop.Review(ctx, Envelope{})
		if err == nil && held == 0 {
			t.Fatal("returned verdict is uncharged")
		}
		if err != nil && held != 0 {
			t.Fatal("failed decode leaked reservation")
		}
		verdict.Release()
		verdict.Release()
		if held != 0 {
			t.Fatal(held)
		}
	}
}
