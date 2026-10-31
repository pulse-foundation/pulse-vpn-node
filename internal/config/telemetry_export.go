package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pulse-foundation/pulse-vpn-node/internal/telemetry"
)

// LoadTelemetryExport reads only an administrator-installed private file. A
// missing file means disabled; malformed data never becomes a core startup error.
func LoadTelemetryExport(path string) (*telemetry.ExportConfig, error) {
	invalid := errors.New("invalid private telemetry enrollment")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, invalid
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16384 {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil || len(data) > 16384 || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, invalid
	}
	var cfg telemetry.ExportConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, invalid
	}
	if err := cfg.Validate(); err != nil {
		return nil, invalid
	}
	return &cfg, nil
}
