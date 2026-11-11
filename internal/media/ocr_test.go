package media

import "testing"

func TestOCRRejectsUnsupportedBeforeUpload(t *testing.T) {
	for _, raw := range []string{`{"document":{"type":"document_url","document_url":"https://example.com/private.pdf"}}`, `{"document":{"type":"document_url","document_url":"data:application/pdf;base64,cGRm"},"required_outputs":["bounding_boxes"]}`} {
		if _, _, err := Prepare([]byte(raw), "ocr", []string{"text"}); err == nil {
			t.Fatal("unsupported request accepted")
		}
	}
}
func TestOCRMultipageTableAndCoordinates(t *testing.T) {
	raw := []byte(`{"pages":[{"index":0,"markdown":"first","tables":[{"id":"table-0","content":"<table><tr><td>A</td></tr></table>","format":"html"}],"dimensions":{"width":100,"height":200},"blocks":[{"top_left_x":0,"top_left_y":10,"bottom_right_x":50,"bottom_right_y":40}]},{"index":1,"markdown":"second","tables":[],"dimensions":{"width":100,"height":200},"blocks":[]}]}`)
	if err := ValidateResponse(raw, []string{"text", "tables", "bounding_boxes"}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{"pages":[{"index":1,"markdown":"wrong"}]}`, `{"pages":[{"index":0,"markdown":"missing boxes"}]}`} {
		if ValidateResponse([]byte(invalid), []string{"bounding_boxes"}) == nil {
			t.Fatal("invalid output accepted")
		}
	}
}
