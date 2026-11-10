package config

import (
	"fmt"
	"strings"
)

// GatewayCatalogIDs are the in-tree catalog ids the gateway matches against
// a lease ticket's catalog_id. The source list is hypermesh-host
// internal/catalog/catalog.go. A gateway tier's model must be one of these
// ids. None of them carry a published speed figure.
var GatewayCatalogIDs = []string{
	"llama-3.1-8b-q4",
	"llama-3.1-70b-q4",
	"qwen-2.5-32b-q4",
	"whisper-small",
	"embed-minilm",
}

// KnownCatalogID reports whether id is a gateway catalog id.
func KnownCatalogID(id string) bool {
	id = strings.TrimSpace(id)
	for _, known := range GatewayCatalogIDs {
		if id == known {
			return true
		}
	}
	return false
}

func catalogList() string {
	return strings.Join(GatewayCatalogIDs, ", ")
}

// ResolveCatalogModel returns the catalog id a gateway tier writes over the
// client's model. catalogModel maps an operator alias onto that id.
func ResolveCatalogModel(model, catalogModel string) (string, error) {
	model = strings.TrimSpace(model)
	catalogModel = strings.TrimSpace(catalogModel)
	if catalogModel != "" {
		if !KnownCatalogID(catalogModel) {
			return "", errCatalog(catalogModel)
		}
		if KnownCatalogID(model) && model != catalogModel {
			return "", fmt.Errorf("model %q and catalog_model %q are different catalog ids", model, catalogModel)
		}
		return catalogModel, nil
	}
	if KnownCatalogID(model) {
		return model, nil
	}
	return "", errCatalog(model)
}

func errCatalog(id string) error {
	return fmt.Errorf("model %q is not a gateway catalog id (%s); set model to a catalog id or set catalog_model", id, catalogList())
}
