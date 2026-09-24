package persistence

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func atomicJSON(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".cpra-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = json.NewEncoder(f).Encode(value); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return replaceDurableFile(name, path)
}
