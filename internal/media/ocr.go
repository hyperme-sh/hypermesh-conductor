// Package media defines the native OCR operation without chat translation.
package media

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type Request struct {
	Document struct {
		Type        string `json:"type"`
		DocumentURL string `json:"document_url,omitempty"`
		ImageURL    string `json:"image_url,omitempty"`
	} `json:"document"`
	Required  []string `json:"required_outputs"`
	LocalOnly bool     `json:"local_only"`
}

func Prepare(raw []byte, model string, outputs []string) ([]byte, Request, error) {
	var request Request
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		return nil, request, fmt.Errorf("invalid OCR request")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, request, fmt.Errorf("invalid OCR document")
	}
	for _, field := range request.Required {
		if !slices.Contains(outputs, field) {
			return nil, request, fmt.Errorf("unsupported required OCR output %s", field)
		}
	}
	value := request.Document.DocumentURL
	allowed := []string{"data:application/pdf;base64,"}
	if request.Document.Type == "image_url" {
		value = request.Document.ImageURL
		allowed = []string{"data:image/png;base64,", "data:image/jpeg;base64,"}
	} else if request.Document.Type != "document_url" {
		return nil, request, fmt.Errorf("unsupported OCR input format")
	}
	valid := false
	for _, prefix := range allowed {
		if strings.HasPrefix(value, prefix) {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
			valid = err == nil && len(decoded) > 0
			break
		}
	}
	if !valid {
		return nil, request, fmt.Errorf("OCR accepts inline PDF, PNG or JPEG only")
	}
	if request.Document.Type == "image_url" && request.Document.DocumentURL != "" || request.Document.Type == "document_url" && request.Document.ImageURL != "" {
		return nil, request, fmt.Errorf("ambiguous OCR document")
	}
	body := map[string]any{"model": model, "document": request.Document, "include_image_base64": false, "include_blocks": slices.Contains(request.Required, "bounding_boxes")}
	if slices.Contains(request.Required, "tables") {
		body["table_format"] = "html"
	}
	wire, err := json.Marshal(body)
	return wire, request, err
}
func ValidateResponse(raw []byte, required []string) error {
	var response struct {
		Pages []struct {
			Index    *int    `json:"index"`
			Markdown *string `json:"markdown"`
			Tables   *[]struct {
				ID      string `json:"id"`
				Content string `json:"content"`
				Format  string `json:"format"`
			} `json:"tables"`
			Dimensions struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"dimensions"`
			Blocks *[]struct {
				X      *int `json:"top_left_x"`
				Y      *int `json:"top_left_y"`
				Right  *int `json:"bottom_right_x"`
				Bottom *int `json:"bottom_right_y"`
			} `json:"blocks"`
		} `json:"pages"`
	}
	if json.Unmarshal(raw, &response) != nil || len(response.Pages) == 0 {
		return fmt.Errorf("invalid OCR response")
	}
	for i, page := range response.Pages {
		if page.Index == nil || *page.Index != i || page.Markdown == nil {
			return fmt.Errorf("invalid OCR page order or text")
		}
		if slices.Contains(required, "tables") {
			if page.Tables == nil {
				return fmt.Errorf("missing OCR tables")
			}
			for _, table := range *page.Tables {
				if table.ID == "" || table.Format != "html" {
					return fmt.Errorf("invalid OCR table")
				}
			}
		}
		if slices.Contains(required, "bounding_boxes") {
			if page.Blocks == nil || page.Dimensions.Width <= 0 || page.Dimensions.Height <= 0 {
				return fmt.Errorf("missing OCR block coordinates")
			}
			for _, block := range *page.Blocks {
				if block.X == nil || block.Y == nil || block.Right == nil || block.Bottom == nil || *block.X < 0 || *block.Y < 0 || *block.Right < *block.X || *block.Bottom < *block.Y || *block.Right > page.Dimensions.Width || *block.Bottom > page.Dimensions.Height {
					return fmt.Errorf("invalid OCR bounding box")
				}
			}
		}
	}
	return nil
}
