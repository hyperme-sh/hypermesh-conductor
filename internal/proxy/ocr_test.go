package proxy

import (
	"github.com/hyperme-sh/hypermesh-conductor/internal/capabilities"
	"github.com/hyperme-sh/hypermesh-conductor/internal/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeOCRPublicContract(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/ocr" {
			t.Error(r.URL.Path)
		}
		io.WriteString(w, `{"pages":[{"index":0,"markdown":"first","tables":[{"id":"t0","format":"html","content":"<table><tr><td>A</td></tr></table>"}],"dimensions":{"width":100,"height":100},"blocks":[{"top_left_x":1,"top_left_y":2,"bottom_right_x":40,"bottom_right_y":20}]},{"index":1,"markdown":"second","tables":[],"dimensions":{"width":100,"height":100},"blocks":[]}]}`)
	}))
	defer server.Close()
	cfg := loadTestConfig(t, t.TempDir(), server.URL+"/v1", server.URL+"/v1", config.AuthPassthrough, config.ProviderOpenAICompatible)
	cfg.Cheap.RuntimeRevision = "ocr-4"
	cfg.Cheap.OCROutputs = []string{"text", "tables", "bounding_boxes"}
	cfg.Cheap.Capabilities = &capabilities.Record{Model: cfg.Cheap.Model, Runtime: "ocr-4", Protocol: cfg.Cheap.Provider, Local: true, Features: map[string]string{"ocr": "verified"}}
	p := New(cfg, nil, nil)
	body := `{"document":{"type":"document_url","document_url":"data:application/pdf;base64,cGRm"},"required_outputs":["text","tables","bounding_boxes"],"local_only":true}`
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(body)))
	if rec.Code != 200 || calls != 1 || !strings.Contains(rec.Body.String(), "<table>") {
		t.Fatalf("%d %d %s", rec.Code, calls, rec.Body.String())
	}
	cfg.Cheap.Capabilities.Local = false
	p = New(cfg, nil, nil)
	rec = httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/ocr", strings.NewReader(body)))
	if rec.Code != 422 || calls != 1 {
		t.Fatal("local-only document uploaded remotely")
	}
}
