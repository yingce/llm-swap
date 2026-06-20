package agent

import (
	"encoding/json"
	"errors"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"llm-swap/internal/config"
)

const markerName = ".llm-agent-artifact.json"

type Marker struct {
	Model         string `json:"model"`
	Object        string `json:"object"`
	Kind          string `json:"kind"`
	CRC64ECMA     string `json:"crc64ecma"`
	InstalledPath string `json:"installed_path"`
}

func WriteMarker(dir, model string, artifact config.Artifact) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	marker := Marker{
		Model:         model,
		Object:        artifact.Object,
		Kind:          artifact.Kind,
		CRC64ECMA:     artifact.CRC64ECMA,
		InstalledPath: dir,
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, markerName), append(data, '\n'), 0o644)
}

func MarkerMatches(dir, model string, artifact config.Artifact) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, markerName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return false, err
	}

	return marker.Model == model &&
		marker.Object == artifact.Object &&
		marker.Kind == artifact.Kind &&
		marker.CRC64ECMA == artifact.CRC64ECMA, nil
}

func CRC64ECMAFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return strconv.FormatUint(hash.Sum64(), 10), nil
}
