package agent

import (
	"hash/crc64"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"llm-swap/internal/config"
)

func TestMarkerMatchSkipsDownload(t *testing.T) {
	dir := t.TempDir()
	artifact := config.Artifact{
		Object:    "models/llama.gguf",
		Kind:      "file",
		CRC64ECMA: "123456789",
	}

	if err := WriteMarker(dir, "llama", artifact); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}

	matches, err := MarkerMatches(dir, "llama", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("MarkerMatches() = false, want true")
	}

	markerPath := filepath.Join(dir, markerName)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("marker file was not written: %v", err)
	}
}

func TestMarkerMatchesMismatchReturnsFalse(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		artifact config.Artifact
	}{
		{
			name:  "different model",
			model: "other",
			artifact: config.Artifact{
				Object:    "models/llama.gguf",
				Kind:      "file",
				CRC64ECMA: "123456789",
			},
		},
		{
			name:  "different object",
			model: "llama",
			artifact: config.Artifact{
				Object:    "models/other.gguf",
				Kind:      "file",
				CRC64ECMA: "123456789",
			},
		},
		{
			name:  "different kind",
			model: "llama",
			artifact: config.Artifact{
				Object:    "models/llama.gguf",
				Kind:      "tar_gz",
				CRC64ECMA: "123456789",
			},
		},
		{
			name:  "different crc64ecma",
			model: "llama",
			artifact: config.Artifact{
				Object:    "models/llama.gguf",
				Kind:      "file",
				CRC64ECMA: "987654321",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			artifact := config.Artifact{
				Object:    "models/llama.gguf",
				Kind:      "file",
				CRC64ECMA: "123456789",
			}
			if err := WriteMarker(dir, "llama", artifact); err != nil {
				t.Fatalf("WriteMarker() error = %v", err)
			}

			matches, err := MarkerMatches(dir, tt.model, tt.artifact)
			if err != nil {
				t.Fatalf("MarkerMatches() error = %v", err)
			}
			if matches {
				t.Fatalf("MarkerMatches() = true, want false")
			}
		})
	}
}

func TestMarkerMatchesMissingMarkerReturnsFalseNil(t *testing.T) {
	matches, err := MarkerMatches(t.TempDir(), "llama", config.Artifact{
		Object:    "models/llama.gguf",
		Kind:      "file",
		CRC64ECMA: "123456789",
	})
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if matches {
		t.Fatalf("MarkerMatches() = true, want false")
	}
}

func TestMarkerMatchesInvalidJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte("{"), 0o644); err != nil {
		t.Fatalf("write invalid marker: %v", err)
	}

	matches, err := MarkerMatches(dir, "llama", config.Artifact{
		Object:    "models/llama.gguf",
		Kind:      "file",
		CRC64ECMA: "123456789",
	})
	if err == nil {
		t.Fatalf("MarkerMatches() error = nil, want error")
	}
	if matches {
		t.Fatalf("MarkerMatches() = true, want false")
	}
}

func TestCRC64ECMAString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	payload := []byte("abc")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	got, err := CRC64ECMAFile(path)
	if err != nil {
		t.Fatalf("CRC64ECMAFile() error = %v", err)
	}

	table := crc64.MakeTable(crc64.ECMA)
	want := strconv.FormatUint(crc64.Checksum(payload, table), 10)
	if got != want {
		t.Fatalf("CRC64ECMAFile() = %q, want %q", got, want)
	}
}
