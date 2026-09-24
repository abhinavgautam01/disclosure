package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func loadCustomTools(path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read custom tools file %q: %w", path, err)
	}

	var config struct {
		Version     int      `json:"version"`
		CustomTools []string `json:"custom_tools"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("parse custom tools file %q: %w", path, err)
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("custom tools file %q must contain a single JSON object", path)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("custom tools file %q: version must be 1 (got %d)", path, config.Version)
	}
	if config.CustomTools == nil {
		return nil, fmt.Errorf("custom tools file %q: custom_tools must be an array of tool or model names", path)
	}
	return config.CustomTools, nil
}
