package capabilitydispatch

import (
	"context"
	"io"
	"path/filepath"

	"github.com/hyperme-sh/hypermesh-conductor/internal/notes"
)

type Config struct {
	Schema     int    `json:"schema"`
	Notes      string `json:"notes"`
	IndexDB    string `json:"index_db"`
	Journal    string `json:"journal"`
	Bridge     string `json:"bridge"`
	Controller string `json:"controller"`
}

func Load(path string) (Config, error) {
	var result Config
	file, err := ownedFile(path)
	if err != nil {
		return result, Refused
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(raw) > 16384 || Decode(raw, &result) != nil || result.Schema != 1 {
		return Config{}, Refused
	}
	for _, path := range []string{result.Notes, result.IndexDB, result.Journal, result.Bridge, result.Controller} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return Config{}, Refused
		}
	}
	if privateDirectory(filepath.Dir(result.IndexDB)) != nil || result.IndexDB == result.Journal {
		return Config{}, Refused
	}
	return result, nil
}
func Open(config Config) (Dispatcher, func(), error) {
	var result Dispatcher
	store, err := notes.OpenCollection(config.Notes, config.IndexDB)
	if err != nil {
		return result, nil, Refused
	}
	catalog, err := store.SyncCapabilities(config.Notes)
	store.Close()
	if err != nil {
		return result, nil, Refused
	}
	bridge, err := RootIPC(config.Bridge)
	if err != nil {
		return result, nil, Refused
	}
	controller, err := RootIPC(config.Controller)
	if err != nil {
		return result, nil, Refused
	}
	journal, err := OpenJournal(config.Journal)
	if err != nil {
		return result, nil, Refused
	}
	return Dispatcher{Catalog: catalog, Controller: controller, Reviewer: bridge, Journal: journal}, func() { journal.Close() }, nil
}

// List exposes only owner-enabled verb bindings that still match the catalog.
// It does not contact a controller or obtain an execution grant.
func List(ctx context.Context, config Config) ([]notes.CapabilityEntry, error) {
	store, err := notes.OpenCollection(config.Notes, config.IndexDB)
	if err != nil {
		return nil, Refused
	}
	defer store.Close()
	catalog, err := store.SyncCapabilities(config.Notes)
	if err != nil {
		return nil, Refused
	}
	bridge, err := RootIPC(config.Bridge)
	if err != nil {
		return nil, Refused
	}
	enabled, err := bridge.bindings(ctx)
	if err != nil {
		return nil, Refused
	}
	result := []notes.CapabilityEntry{}
	for _, binding := range enabled {
		if _, err = catalog.Requirements(binding.note()); err != nil {
			continue
		}
		entries, err := catalog.Snapshot(binding.Scope)
		if err != nil {
			return nil, Refused
		}
		for _, entry := range entries {
			if entry.Definition.ID == binding.ID {
				result = append(result, entry)
				break
			}
		}
	}
	return result, nil
}
