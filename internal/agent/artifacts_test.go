package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestInstallArtifactMarkerSkipAvoidsGET(t *testing.T) {
	modelRoot := t.TempDir()
	modelName := "llama"
	modelDir := filepath.Join(modelRoot, modelName)
	artifact := config.Artifact{
		Object:    "models/llama.gguf",
		Kind:      "file",
		CRC64ECMA: "123456789",
	}
	if err := WriteMarker(modelDir, modelName, artifact); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}

	var getCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			getCount++
		}
		t.Fatalf("unexpected network request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, modelName, artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if getCount != 0 {
		t.Fatalf("GET count = %d, want 0", getCount)
	}
}

func TestInstallArtifactFileWritesBasenameAndMarker(t *testing.T) {
	payload := []byte("model payload")
	crc := crc64String(payload)
	artifact := config.Artifact{
		Object:    "nested/Qwen3.6-35B-A3B-RP-NSFW-q4_K_M.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	}
	var gotMethods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethods = append(gotMethods, r.Method)
		if r.URL.Path != "/nested/Qwen3.6-35B-A3B-RP-NSFW-q4_K_M.gguf" {
			t.Fatalf("path = %q, want artifact object path", r.URL.Path)
		}
		w.Header().Set("x-oss-hash-crc64ecma", crc)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL+"/", modelRoot, "qwen3.6", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	modelDir := filepath.Join(modelRoot, "qwen3.6")
	modelPath := filepath.Join(modelDir, "Qwen3.6-35B-A3B-RP-NSFW-q4_K_M.gguf")
	got, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("read installed file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("installed payload = %q, want %q", got, payload)
	}
	matches, err := MarkerMatches(modelDir, "qwen3.6", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("marker does not match installed artifact")
	}
	if strings.Join(gotMethods, ",") != "HEAD,GET" {
		t.Fatalf("methods = %v, want HEAD then GET", gotMethods)
	}
}

func TestInstallArtifactTarGzExtractsFiles(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"config.json":       `{"ok":true}`,
		"weights/model.bin": "weights",
	})
	crc := crc64String(archive)
	artifact := config.Artifact{
		Object:    "archives/model.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, archive, crc)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	modelDir := filepath.Join(modelRoot, "qwen")
	for path, want := range map[string]string{
		"config.json":       `{"ok":true}`,
		"weights/model.bin": "weights",
	} {
		got, err := os.ReadFile(filepath.Join(modelDir, path))
		if err != nil {
			t.Fatalf("read extracted %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	matches, err := MarkerMatches(modelDir, "qwen", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("marker does not match installed artifact")
	}
}

func TestInstallArtifactTarGzBlocksPathTraversal(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"../escape.txt": "escape",
	})
	crc := crc64String(archive)
	artifact := config.Artifact{
		Object:    "archives/bad.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, archive, crc)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err == nil {
		t.Fatalf("InstallArtifact() error = nil, want path traversal error")
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if _, statErr := os.Stat(filepath.Join(modelRoot, "escape.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("escape path exists or stat failed unexpectedly: %v", statErr)
	}
}

func TestInstallArtifactHeadCRCMismatchErrorsBeforeGET(t *testing.T) {
	payload := []byte("model payload")
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc64String(payload),
	}
	var gotGET bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("x-oss-hash-crc64ecma", "wrong")
		case http.MethodGet:
			gotGET = true
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, t.TempDir(), "qwen", artifact)
	if err == nil {
		t.Fatalf("InstallArtifact() error = nil, want CRC mismatch error")
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if gotGET {
		t.Fatalf("GET was called after HEAD CRC mismatch")
	}
}

func TestInstallArtifactAbsentHeadCRCVerifiesGETBody(t *testing.T) {
	payload := []byte("payload without head crc")
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc64String(payload),
	}
	var sawHead bool
	var sawGet bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			sawHead = true
		case http.MethodGet:
			sawGet = true
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}
	if !sawHead || !sawGet {
		t.Fatalf("saw HEAD=%v GET=%v, want both", sawHead, sawGet)
	}
	if got, err := CRC64ECMAFile(filepath.Join(modelRoot, "qwen", "model.gguf")); err != nil || got != artifact.CRC64ECMA {
		t.Fatalf("installed CRC = %q, %v; want %q", got, err, artifact.CRC64ECMA)
	}
}

func crc64String(payload []byte) string {
	table := crc64.MakeTable(crc64.ECMA)
	return strconv.FormatUint(crc64.Checksum(payload, table), 10)
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(body)),
		}); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatalf("write tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func artifactServer(t *testing.T, payload []byte, crc string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" || r.URL.Path == "/" {
			t.Fatalf("empty artifact path")
		}
		w.Header().Set("x-oss-hash-crc64ecma", crc)
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			_, _ = w.Write(payload)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
}
