package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"os"
	slashpath "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"llm-swap/internal/config"
)

const markerName = ".llm-agent-artifact.json"
const ossCRC64Header = "x-oss-hash-crc64ecma"

var writeMarkerFile = os.WriteFile

type Marker struct {
	Model         string    `json:"model"`
	Object        string    `json:"object"`
	Kind          string    `json:"kind"`
	CRC64ECMA     string    `json:"crc64ecma"`
	InstalledPath string    `json:"installed_path"`
	InstalledAt   time.Time `json:"installed_at"`
}

type ArtifactProgress struct {
	DownloadedBytes int64
	TotalBytes      int64
	Percent         float64
}

type ArtifactProgressFunc func(ArtifactProgress)

type artifactProgressTracker struct {
	total      int64
	nextPct    float64
	lastEmit   time.Time
	now        func() time.Time
	onProgress ArtifactProgressFunc
}

func newArtifactProgressTracker(total int64, onProgress ArtifactProgressFunc) *artifactProgressTracker {
	return &artifactProgressTracker{
		total:      total,
		nextPct:    5,
		now:        time.Now,
		onProgress: onProgress,
	}
}

func (t *artifactProgressTracker) Observe(downloaded int64) {
	if t == nil || t.onProgress == nil || downloaded <= 0 {
		return
	}

	now := t.now()
	percent := float64(0)
	if t.total > 0 {
		percent = float64(downloaded) * 100 / float64(t.total)
	}
	if t.total > 0 && percent > 100 {
		percent = 100
	}

	shouldEmit := false
	if t.total > 0 && percent >= t.nextPct {
		shouldEmit = true
		for t.nextPct <= percent {
			t.nextPct += 5
		}
	}
	if t.total <= 0 && t.lastEmit.IsZero() {
		shouldEmit = true
	}
	if !t.lastEmit.IsZero() && now.Sub(t.lastEmit) >= time.Minute {
		shouldEmit = true
	}

	if !shouldEmit {
		return
	}

	t.lastEmit = now
	t.onProgress(ArtifactProgress{
		DownloadedBytes: downloaded,
		TotalBytes:      t.total,
		Percent:         percent,
	})
}

type artifactProgressReader struct {
	reader     io.Reader
	downloaded int64
	tracker    *artifactProgressTracker
}

func (r *artifactProgressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
		r.tracker.Observe(r.downloaded)
	}
	return n, err
}

func WriteMarker(dir, model string, artifact config.Artifact) error {
	return writeMarker(dir, dir, model, artifact)
}

func writeMarker(dir, installedPath, model string, artifact config.Artifact) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	marker := Marker{
		Model:         model,
		Object:        artifact.Object,
		Kind:          artifact.Kind,
		CRC64ECMA:     artifact.CRC64ECMA,
		InstalledPath: installedPath,
		InstalledAt:   time.Now().UTC(),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}

	return writeMarkerFile(filepath.Join(dir, markerName), append(data, '\n'), 0o644)
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

func InstallArtifact(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName string, artifact config.Artifact) (bool, error) {
	return InstallArtifactWithProgress(ctx, httpClient, ossBaseURL, modelRoot, modelName, artifact, nil)
}

func InstallArtifactWithProgress(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName string, artifact config.Artifact, onProgress ArtifactProgressFunc) (bool, error) {
	modelDir := filepath.Join(modelRoot, modelName)
	matches, err := MarkerMatches(modelDir, modelName, artifact)
	if err != nil {
		return false, err
	}
	if matches {
		return false, nil
	}

	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if err := os.MkdirAll(modelRoot, 0o755); err != nil {
		return false, err
	}

	downloadURL := artifactURL(ossBaseURL, artifact.Object)
	if err := checkRemoteCRC(ctx, httpClient, downloadURL, artifact.CRC64ECMA); err != nil {
		return false, err
	}

	tmpFile, err := downloadArtifact(ctx, httpClient, downloadURL, modelRoot, onProgress)
	if err != nil {
		return false, err
	}
	defer os.Remove(tmpFile)

	gotCRC, err := CRC64ECMAFile(tmpFile)
	if err != nil {
		return false, err
	}
	if gotCRC != artifact.CRC64ECMA {
		return false, fmt.Errorf("downloaded artifact crc64ecma mismatch for %s: got %s, want %s", artifact.Object, gotCRC, artifact.CRC64ECMA)
	}

	switch artifact.Kind {
	case "file":
		if err := installFileArtifact(tmpFile, modelDir, modelName, artifact); err != nil {
			return false, err
		}
	case "tar_gz":
		if err := installTarGzArtifact(tmpFile, modelRoot, modelDir, modelName, artifact); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported artifact kind %q", artifact.Kind)
	}

	return true, nil
}

func artifactURL(ossBaseURL, object string) string {
	return strings.TrimRight(ossBaseURL, "/") + "/" + strings.TrimLeft(object, "/")
}

func checkRemoteCRC(ctx context.Context, httpClient *http.Client, downloadURL, wantCRC string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HEAD %s returned %s", downloadURL, resp.Status)
	}
	gotCRC := strings.TrimSpace(resp.Header.Get(ossCRC64Header))
	if gotCRC == "" {
		return fmt.Errorf("HEAD %s missing %s", downloadURL, ossCRC64Header)
	}
	if gotCRC != wantCRC {
		return fmt.Errorf("HEAD crc64ecma mismatch for %s: got %s, want %s", downloadURL, gotCRC, wantCRC)
	}
	return nil
}

func downloadArtifact(ctx context.Context, httpClient *http.Client, downloadURL, tempDir string, onProgress ArtifactProgressFunc) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s returned %s", downloadURL, resp.Status)
	}

	tmp, err := os.CreateTemp(tempDir, ".llm-agent-artifact-*.download")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	reader := io.Reader(resp.Body)
	if onProgress != nil {
		reader = &artifactProgressReader{
			reader:  resp.Body,
			tracker: newArtifactProgressTracker(resp.ContentLength, onProgress),
		}
	}
	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ok = true
	return tmpPath, nil
}

func installFileArtifact(tmpFile, modelDir, modelName string, artifact config.Artifact) error {
	stageDir, err := os.MkdirTemp(filepath.Dir(modelDir), ".llm-agent-artifact-stage-*")
	if err != nil {
		return err
	}
	stageDir = filepath.Clean(stageDir)
	stageMoved := false
	defer func() {
		if !stageMoved {
			_ = os.RemoveAll(stageDir)
		}
	}()

	filename := filepath.Base(filepath.FromSlash(artifact.Object))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		return fmt.Errorf("artifact object %q has no base filename", artifact.Object)
	}
	targetPath := filepath.Join(stageDir, filename)
	if err := os.Chmod(tmpFile, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, targetPath); err != nil {
		return err
	}
	if err := writeMarker(stageDir, modelDir, modelName, artifact); err != nil {
		return err
	}
	if err := replaceDir(stageDir, modelDir); err != nil {
		return err
	}
	stageMoved = true
	return nil
}

func installTarGzArtifact(tmpFile, modelRoot, modelDir, modelName string, artifact config.Artifact) error {
	extractDir, err := os.MkdirTemp(modelRoot, ".llm-agent-artifact-extract-*")
	if err != nil {
		return err
	}
	extractDir = filepath.Clean(extractDir)
	extractMoved := false
	defer func() {
		if !extractMoved {
			_ = os.RemoveAll(extractDir)
		}
	}()

	if err := extractTarGz(tmpFile, extractDir); err != nil {
		return err
	}

	if err := flattenSingleTopLevelDir(extractDir); err != nil {
		return err
	}

	if err := writeMarker(extractDir, modelDir, modelName, artifact); err != nil {
		return err
	}

	if err := replaceDir(extractDir, modelDir); err != nil {
		return err
	}
	extractMoved = true
	return nil
}

func flattenSingleTopLevelDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var rootDir os.DirEntry
	for _, entry := range entries {
		if entry.Name() == markerName {
			continue
		}
		if !entry.IsDir() {
			return nil
		}
		if rootDir != nil {
			return nil
		}
		rootDir = entry
	}
	if rootDir == nil {
		return nil
	}

	rootPath := filepath.Join(dir, rootDir.Name())
	rootEntries, err := os.ReadDir(rootPath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, markerName)); err != nil {
		return err
	}
	for _, entry := range rootEntries {
		if err := os.Rename(filepath.Join(rootPath, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(rootPath)
}

func extractTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractTarEntry(tr, header, destDir); err != nil {
			return err
		}
	}
}

func extractTarEntry(reader io.Reader, header *tar.Header, destDir string) error {
	cleanName := slashpath.Clean(header.Name)
	if cleanName == "." {
		return nil
	}
	if slashpath.IsAbs(header.Name) || cleanName == ".." || strings.HasPrefix(cleanName, "../") {
		return fmt.Errorf("tar entry %q escapes destination", header.Name)
	}

	targetPath := filepath.Join(destDir, filepath.FromSlash(cleanName))
	rel, err := filepath.Rel(destDir, targetPath)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("tar entry %q escapes destination", header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(targetPath, 0o755)
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, sanitizedFileMode(header.Mode))
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, reader); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	default:
		return fmt.Errorf("unsupported tar entry %q type %d", header.Name, header.Typeflag)
	}
}

func sanitizedFileMode(mode int64) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func replaceDir(newDir, targetDir string) error {
	if err := os.MkdirAll(filepath.Dir(targetDir), 0o755); err != nil {
		return err
	}

	backupDir := ""
	if _, err := os.Stat(targetDir); err == nil {
		var mkErr error
		backupDir, mkErr = os.MkdirTemp(filepath.Dir(targetDir), ".llm-agent-artifact-backup-*")
		if mkErr != nil {
			return mkErr
		}
		if rmErr := os.Remove(backupDir); rmErr != nil {
			return rmErr
		}
		if err := os.Rename(targetDir, backupDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.Rename(newDir, targetDir); err != nil {
		if backupDir != "" {
			_ = os.Rename(backupDir, targetDir)
		}
		return err
	}

	if backupDir != "" {
		return os.RemoveAll(backupDir)
	}
	return nil
}
