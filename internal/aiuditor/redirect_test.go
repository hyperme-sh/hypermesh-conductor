package aiuditor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHTTPReviewDoesNotReplayRedirectedPrompts(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write([]byte(`{"action":"allow"}`)) }))
	defer target.Close()
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, injected := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", status, injected), func(t *testing.T) {
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
				defer source.Close()
				hop := HTTP{URL: source.URL}
				if injected {
					hop.Client = &http.Client{}
				}
				verdict, err := hop.Review(context.Background(), Envelope{})
				verdict.Release()
				if err == nil || hits.Load() != 0 {
					t.Fatal(err, hits.Load())
				}
			})
		}
	}
}
