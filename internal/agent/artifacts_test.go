package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"llm-swap/internal/config"
)

func TestArtifactProgressTrackerReportsEveryFivePercentOrMinute(t *testing.T) {
	now := time.Unix(100, 0)
	var reports []ArtifactProgress
	tracker := newArtifactProgressTracker(100, func(progress ArtifactProgress) {
		reports = append(reports, progress)
	})
	tracker.now = func() time.Time { return now }

	tracker.Observe(4)
	if len(reports) != 0 {
		t.Fatalf("reports after 4%% = %d, want 0", len(reports))
	}

	tracker.Observe(5)
	if len(reports) != 1 || reports[0].DownloadedBytes != 5 || reports[0].Percent != 5 {
		t.Fatalf("first report = %+v, want 5 bytes / 5%%", reports)
	}

	tracker.Observe(9)
	if len(reports) != 1 {
		t.Fatalf("reports after 9%% = %d, want 1", len(reports))
	}

	now = now.Add(time.Minute)
	tracker.Observe(10)
	if len(reports) != 2 || reports[1].DownloadedBytes != 10 || reports[1].Percent != 10 {
		t.Fatalf("minute report = %+v, want 10 bytes / 10%%", reports)
	}

	tracker.Observe(14)
	if len(reports) != 2 {
		t.Fatalf("reports after 14%% = %d, want 2", len(reports))
	}

	tracker.Observe(15)
	if len(reports) != 3 || reports[2].DownloadedBytes != 15 || reports[2].Percent != 15 {
		t.Fatalf("threshold report = %+v, want 15 bytes / 15%%", reports)
	}
}

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

func TestInstallArtifactAtUsesCustomDirectory(t *testing.T) {
	payload := []byte("versioned model payload")
	crc := crc64String(payload)
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, payload, crc)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifactAt(
		context.Background(),
		server.Client(),
		server.URL,
		modelRoot,
		"joyfox-model-v2",
		"joyfox-model-20260720",
		artifact,
	)
	if err != nil {
		t.Fatalf("InstallArtifactAt() error = %v", err)
	}
	if !installed {
		t.Fatal("InstallArtifactAt() installed = false, want true")
	}

	modelDir := filepath.Join(modelRoot, "joyfox-model-20260720")
	got, err := os.ReadFile(filepath.Join(modelDir, "model.gguf"))
	if err != nil {
		t.Fatalf("read installed payload: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("installed payload = %q, want %q", got, payload)
	}
	matches, err := MarkerMatches(modelDir, "joyfox-model-v2", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatal("marker does not preserve canonical model identity")
	}
	if _, err := os.Stat(filepath.Join(modelRoot, "joyfox-model-v2")); !os.IsNotExist(err) {
		t.Fatalf("canonical model directory exists or stat failed unexpectedly: %v", err)
	}
}

func TestInstallArtifactReusesExistingSourceFileInModelRoot(t *testing.T) {
	payload := []byte("model payload already present")
	crc := crc64String(payload)
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	}
	modelRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(modelRoot, "model.gguf"), payload, 0o644); err != nil {
		t.Fatalf("write source artifact: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected network request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(modelRoot, "qwen", "model.gguf"))
	if err != nil {
		t.Fatalf("read installed model file: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("installed payload = %q, want %q", got, payload)
	}
	matches, err := MarkerMatches(filepath.Join(modelRoot, "qwen"), "qwen", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("marker does not match installed artifact")
	}
}

func TestInstallArtifactPersistsDownloadedSourceFileInModelRoot(t *testing.T) {
	payload := []byte("download once keep source")
	crc := crc64String(payload)
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, payload, crc)
	defer server.Close()
	modelRoot := t.TempDir()

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	got, err := os.ReadFile(filepath.Join(modelRoot, "model.gguf"))
	if err != nil {
		t.Fatalf("read persisted source artifact: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("persisted source = %q, want payload", got)
	}
}

func TestInstallArtifactRechecksMarkerAfterWaitingForLock(t *testing.T) {
	payload := []byte("installed by another worker")
	crc := crc64String(payload)
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc,
	}
	modelRoot := t.TempDir()
	modelDir := filepath.Join(modelRoot, "qwen")
	if err := os.MkdirAll(filepath.Join(modelRoot, ".locks"), 0o755); err != nil {
		t.Fatalf("create lock dir: %v", err)
	}
	lockFile, err := os.OpenFile(filepath.Join(modelRoot, ".locks", artifactLockName("qwen", artifact)+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer lockFile.Close()
	if err := lockArtifactFile(lockFile); err != nil {
		t.Fatalf("lock file: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected network request %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	resultCh := make(chan struct {
		installed bool
		err       error
	}, 1)
	go func() {
		installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
		resultCh <- struct {
			installed bool
			err       error
		}{installed: installed, err: err}
	}()

	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("create model dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "model.gguf"), payload, 0o644); err != nil {
		t.Fatalf("write installed file: %v", err)
	}
	if err := WriteMarker(modelDir, "qwen", artifact); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := unlockArtifactFile(lockFile); err != nil {
		t.Fatalf("unlock file: %v", err)
	}

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("InstallArtifact() error = %v", result.err)
		}
		if result.installed {
			t.Fatalf("InstallArtifact() installed = true, want false after another worker installed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("InstallArtifact() did not return after lock release")
	}
}

func TestInstallArtifactFileMarkerFailurePreservesExistingTarget(t *testing.T) {
	oldArtifact := config.Artifact{
		Object:    "models/old.gguf",
		Kind:      "file",
		CRC64ECMA: "old-crc",
	}
	newPayload := []byte("new model payload")
	newArtifact := config.Artifact{
		Object:    "models/new.gguf",
		Kind:      "file",
		CRC64ECMA: crc64String(newPayload),
	}
	server := artifactServer(t, newPayload, newArtifact.CRC64ECMA)
	defer server.Close()

	modelRoot := t.TempDir()
	modelName := "qwen"
	modelDir := filepath.Join(modelRoot, modelName)
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("create existing model dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "old.gguf"), []byte("old model payload"), 0o644); err != nil {
		t.Fatalf("write existing payload: %v", err)
	}
	if err := WriteMarker(modelDir, modelName, oldArtifact); err != nil {
		t.Fatalf("write existing marker: %v", err)
	}

	writeMarkerFile = func(string, []byte, os.FileMode) error {
		return os.ErrPermission
	}
	t.Cleanup(func() {
		writeMarkerFile = os.WriteFile
	})

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, modelName, newArtifact)
	if err == nil {
		t.Fatalf("InstallArtifact() error = nil, want marker write error")
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if got, err := os.ReadFile(filepath.Join(modelDir, "old.gguf")); err != nil || string(got) != "old model payload" {
		t.Fatalf("existing payload = %q, %v; want old payload", got, err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "new.gguf")); !os.IsNotExist(err) {
		t.Fatalf("new payload exists or stat failed unexpectedly: %v", err)
	}
	matches, err := MarkerMatches(modelDir, modelName, oldArtifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("existing marker was not preserved")
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

func TestInstallArtifactTarGzFlattensSingleTopLevelDirectory(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"Qwen2.5-0.5B/config.json": `{"model_type":"qwen2"}`,
	})
	crc := crc64String(archive)
	artifact := config.Artifact{
		Object:    "archives/qwen2.5.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, archive, crc)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen2.5", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	modelDir := filepath.Join(modelRoot, "qwen2.5")
	got, err := os.ReadFile(filepath.Join(modelDir, "config.json"))
	if err != nil {
		t.Fatalf("read flattened config.json: %v", err)
	}
	if string(got) != `{"model_type":"qwen2"}` {
		t.Fatalf("config.json = %q, want qwen config", got)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "Qwen2.5-0.5B")); !os.IsNotExist(err) {
		t.Fatalf("nested root dir exists or stat failed unexpectedly: %v", err)
	}
	matches, err := MarkerMatches(modelDir, "qwen2.5", artifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("marker does not match installed artifact")
	}
}

func TestInstallArtifactTarGzPreservesMultipleTopLevelEntries(t *testing.T) {
	archive := tarGz(t, map[string]string{
		"Qwen2.5-0.5B/config.json": `{"model_type":"qwen2"}`,
		"README.md":                "model notes",
	})
	crc := crc64String(archive)
	artifact := config.Artifact{
		Object:    "archives/qwen2.5.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: crc,
	}
	server := artifactServer(t, archive, crc)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen2.5", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	modelDir := filepath.Join(modelRoot, "qwen2.5")
	for path, want := range map[string]string{
		"Qwen2.5-0.5B/config.json": `{"model_type":"qwen2"}`,
		"README.md":                "model notes",
	} {
		got, err := os.ReadFile(filepath.Join(modelDir, path))
		if err != nil {
			t.Fatalf("read extracted %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(modelDir, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("flattened config exists or stat failed unexpectedly: %v", err)
	}
}

func TestInstallArtifactTarGzMarkerFailurePreservesExistingTarget(t *testing.T) {
	oldArtifact := config.Artifact{
		Object:    "archives/old.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: "old-crc",
	}
	newArchive := tarGz(t, map[string]string{
		"config.json": "new config",
	})
	newArtifact := config.Artifact{
		Object:    "archives/new.tar.gz",
		Kind:      "tar_gz",
		CRC64ECMA: crc64String(newArchive),
	}
	server := artifactServer(t, newArchive, newArtifact.CRC64ECMA)
	defer server.Close()

	modelRoot := t.TempDir()
	modelName := "qwen"
	modelDir := filepath.Join(modelRoot, modelName)
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatalf("create existing model dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "config.json"), []byte("old config"), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}
	if err := WriteMarker(modelDir, modelName, oldArtifact); err != nil {
		t.Fatalf("write existing marker: %v", err)
	}

	writeMarkerFile = func(string, []byte, os.FileMode) error {
		return os.ErrPermission
	}
	t.Cleanup(func() {
		writeMarkerFile = os.WriteFile
	})

	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, modelName, newArtifact)
	if err == nil {
		t.Fatalf("InstallArtifact() error = nil, want marker write error")
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if got, err := os.ReadFile(filepath.Join(modelDir, "config.json")); err != nil || string(got) != "old config" {
		t.Fatalf("existing config = %q, %v; want old config", got, err)
	}
	matches, err := MarkerMatches(modelDir, modelName, oldArtifact)
	if err != nil {
		t.Fatalf("MarkerMatches() error = %v", err)
	}
	if !matches {
		t.Fatalf("existing marker was not preserved")
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

func TestInstallArtifactMissingHeadCRCErrorsBeforeGET(t *testing.T) {
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
	if err == nil {
		t.Fatalf("InstallArtifact() error = nil, want missing HEAD crc error")
	}
	if installed {
		t.Fatalf("InstallArtifact() installed = true, want false")
	}
	if !sawHead {
		t.Fatalf("saw HEAD=false, want true")
	}
	if sawGet {
		t.Fatalf("GET was called after missing HEAD CRC")
	}
}

func TestInstallArtifactWritesInstalledAtMarker(t *testing.T) {
	payload := []byte("model payload")
	artifact := config.Artifact{
		Object:    "models/model.gguf",
		Kind:      "file",
		CRC64ECMA: crc64String(payload),
	}
	server := artifactServer(t, payload, artifact.CRC64ECMA)
	defer server.Close()

	modelRoot := t.TempDir()
	installed, err := InstallArtifact(context.Background(), server.Client(), server.URL, modelRoot, "qwen", artifact)
	if err != nil {
		t.Fatalf("InstallArtifact() error = %v", err)
	}
	if !installed {
		t.Fatalf("InstallArtifact() installed = false, want true")
	}

	var marker Marker
	data, err := os.ReadFile(filepath.Join(modelRoot, "qwen", markerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	if marker.InstalledAt.IsZero() {
		t.Fatalf("marker installed_at is zero: %+v", marker)
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
