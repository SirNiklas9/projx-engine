package routing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const catalogFileName = "model-catalog.json"

// CatalogPath returns the project-local last-known-good model catalog path.
func CatalogPath(root string) string {
	return filepath.Join(root, ".projx", catalogFileName)
}

// LoadCatalog loads the last-known-good snapshot. A missing catalog is not an
// error; callers can fall back to the static seed configuration.
func LoadCatalog(root string) (ModelCatalog, error) {
	data, err := os.ReadFile(CatalogPath(root))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ModelCatalog{}, nil
		}
		return ModelCatalog{}, err
	}
	var catalog ModelCatalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return ModelCatalog{}, err
	}
	return catalog, nil
}

// SaveCatalog atomically replaces the snapshot. Refreshers should call this
// only after all provider inventories have been normalized and validated.
func SaveCatalog(root string, catalog ModelCatalog) error {
	dir := filepath.Dir(CatalogPath(root))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".model-catalog-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, CatalogPath(root))
}
