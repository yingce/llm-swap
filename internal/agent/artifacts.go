package agent

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type artifactInstallFence struct {
	ConfigRevision      int64  `json:"config_revision"`
	ArtifactFingerprint string `json:"artifact_fingerprint"`
}

type artifactInstallSupersededError struct {
	Revision           int64
	Fingerprint        string
	WinningRevision    int64
	WinningFingerprint string
}

func (e *artifactInstallSupersededError) Error() string {
	return fmt.Sprintf(
		"artifact install revision %d fingerprint %s was superseded by revision %d fingerprint %s",
		e.Revision,
		e.Fingerprint,
		e.WinningRevision,
		e.WinningFingerprint,
	)
}

type artifactInstallObservation struct {
	Event              string
	Fingerprint        string
	WinningFingerprint string
}

type artifactInstallObserver func(artifactInstallObservation)

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

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if contextErr := r.ctx.Err(); contextErr != nil {
			return n, contextErr
		}
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
	return crc64ECMAFileContext(context.Background(), path)
}

func crc64ECMAFileContext(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := crc64.New(crc64.MakeTable(crc64.ECMA))
	if _, err := io.Copy(hash, &contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}

	return strconv.FormatUint(hash.Sum64(), 10), nil
}

func InstallArtifact(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName string, artifact config.Artifact) (bool, error) {
	return InstallArtifactAt(ctx, httpClient, ossBaseURL, modelRoot, modelName, modelName, artifact)
}

func InstallArtifactAt(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName, modelDirName string, artifact config.Artifact) (bool, error) {
	return InstallArtifactWithProgressAt(ctx, httpClient, ossBaseURL, modelRoot, modelName, modelDirName, artifact, nil)
}

func InstallArtifactWithProgress(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName string, artifact config.Artifact, onProgress ArtifactProgressFunc) (bool, error) {
	return InstallArtifactWithProgressAt(ctx, httpClient, ossBaseURL, modelRoot, modelName, modelName, artifact, onProgress)
}

func InstallArtifactWithProgressAt(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName, modelDirName string, artifact config.Artifact, onProgress ArtifactProgressFunc) (bool, error) {
	return installArtifactWithProgressAtRevision(ctx, httpClient, ossBaseURL, modelRoot, modelName, modelDirName, 0, artifact, onProgress, nil)
}

func installArtifactWithProgressAtRevision(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelName, modelDirName string, configRevision int64, artifact config.Artifact, onProgress ArtifactProgressFunc, observe artifactInstallObserver) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if err := os.MkdirAll(modelRoot, 0o755); err != nil {
		return false, err
	}

	desired := artifactInstallFence{
		ConfigRevision:      configRevision,
		ArtifactFingerprint: artifactFingerprint(artifact),
	}
	winner, desiredCurrent, err := publishArtifactInstallFence(ctx, modelRoot, modelDirName, desired)
	if err != nil {
		return false, err
	}
	if !desiredCurrent {
		observeArtifactInstall(observe, artifactInstallObservation{
			Event:              "artifact_install_stale_fence",
			Fingerprint:        desired.ArtifactFingerprint,
			WinningFingerprint: winner.ArtifactFingerprint,
		})
		return false, newArtifactInstallSupersededError(desired, winner)
	}

	modelDir := filepath.Join(modelRoot, modelDirName)
	matches, err := MarkerMatches(modelDir, modelName, artifact)
	if err != nil {
		return false, err
	}
	if matches {
		return false, nil
	}

	sourcePath, targetReady, err := prepareArtifactSource(ctx, httpClient, ossBaseURL, modelRoot, modelDir, modelName, artifact, onProgress)
	if err != nil {
		return false, err
	}
	if targetReady {
		return false, nil
	}

	var stageDir string
	switch artifact.Kind {
	case "file":
		stageDir, err = stageFileArtifact(ctx, sourcePath, modelDir, artifact)
	case "tar_gz":
		stageDir, err = stageTarGzArtifact(ctx, sourcePath, modelRoot)
	default:
		return false, fmt.Errorf("unsupported artifact kind %q", artifact.Kind)
	}
	if err != nil {
		return false, err
	}
	stageMoved := false
	defer func() {
		if !stageMoved {
			_ = os.RemoveAll(stageDir)
		}
	}()

	installed, err := commitStagedArtifact(ctx, modelRoot, modelDirName, stageDir, modelDir, modelName, desired, artifact, observe)
	if err != nil {
		return false, err
	}
	stageMoved = installed

	return installed, nil
}

func prepareArtifactSource(ctx context.Context, httpClient *http.Client, ossBaseURL, modelRoot, modelDir, modelName string, artifact config.Artifact, onProgress ArtifactProgressFunc) (string, bool, error) {
	lockFile, err := acquireArtifactLock(ctx, modelRoot, modelName, artifact)
	if err != nil {
		return "", false, err
	}
	defer func() {
		_ = unlockArtifactFile(lockFile)
		_ = lockFile.Close()
	}()

	matches, err := MarkerMatches(modelDir, modelName, artifact)
	if err != nil {
		return "", false, err
	}
	if matches {
		return "", true, nil
	}

	sourcePath, sourceReady, err := localArtifactSourceContext(ctx, modelRoot, artifact)
	if err != nil {
		return "", false, err
	}
	if sourceReady {
		return sourcePath, false, nil
	}

	downloadURL := artifactURL(ossBaseURL, artifact.Object)
	if err := checkRemoteCRC(ctx, httpClient, downloadURL, artifact.CRC64ECMA); err != nil {
		return "", false, err
	}
	tmpFile, err := downloadArtifact(ctx, httpClient, downloadURL, modelRoot, onProgress)
	if err != nil {
		return "", false, err
	}
	tmpMoved := false
	defer func() {
		if !tmpMoved {
			_ = os.Remove(tmpFile)
		}
	}()

	gotCRC, err := crc64ECMAFileContext(ctx, tmpFile)
	if err != nil {
		return "", false, err
	}
	if gotCRC != artifact.CRC64ECMA {
		return "", false, fmt.Errorf("downloaded artifact crc64ecma mismatch for %s: got %s, want %s", artifact.Object, gotCRC, artifact.CRC64ECMA)
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := persistArtifactSource(tmpFile, sourcePath); err != nil {
		return "", false, err
	}
	tmpMoved = true
	return sourcePath, false, nil
}

func artifactURL(ossBaseURL, object string) string {
	return strings.TrimRight(ossBaseURL, "/") + "/" + strings.TrimLeft(object, "/")
}

func artifactLockName(_ string, artifact config.Artifact) string {
	sum := sha256.Sum256([]byte(artifact.Kind + "\x00" + artifact.Object + "\x00" + artifact.CRC64ECMA))
	return hex.EncodeToString(sum[:])
}

func artifactFingerprint(artifact config.Artifact) string {
	return artifactLockName("", artifact)
}

func removedArtifactFingerprint() string {
	sum := sha256.Sum256([]byte("removed"))
	return hex.EncodeToString(sum[:])
}

func modelDirLockName(modelDirName string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(modelDirName)))
	return "model-dir-" + hex.EncodeToString(sum[:])
}

func acquireArtifactLock(ctx context.Context, modelRoot, modelName string, artifact config.Artifact) (*os.File, error) {
	return acquireLockFile(ctx, modelRoot, artifactLockName(modelName, artifact)+".lock")
}

func acquireModelDirLock(ctx context.Context, modelRoot, modelDirName string) (*os.File, error) {
	return acquireLockFile(ctx, modelRoot, modelDirLockName(modelDirName)+".lock")
}

func acquireLockFile(ctx context.Context, modelRoot, lockName string) (*os.File, error) {
	lockDir := filepath.Join(modelRoot, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(lockDir, lockName)
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := lockArtifactFileContext(ctx, lockFile); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return lockFile, nil
}

func publishArtifactInstallFence(ctx context.Context, modelRoot, modelDirName string, desired artifactInstallFence) (artifactInstallFence, bool, error) {
	lockFile, err := acquireModelDirLock(ctx, modelRoot, modelDirName)
	if err != nil {
		return artifactInstallFence{}, false, err
	}
	defer func() {
		_ = unlockArtifactFile(lockFile)
		_ = lockFile.Close()
	}()

	current, exists, err := readArtifactInstallFence(modelRoot, modelDirName)
	if err != nil {
		return artifactInstallFence{}, false, err
	}
	if exists && !artifactFenceShouldAdvance(current, desired) {
		return current, artifactFencesEqual(current, desired), nil
	}
	if err := writeArtifactInstallFence(modelRoot, modelDirName, desired); err != nil {
		return artifactInstallFence{}, false, err
	}
	return desired, true, nil
}

func artifactFenceShouldAdvance(current, desired artifactInstallFence) bool {
	if desired.ConfigRevision > current.ConfigRevision {
		return true
	}
	return desired.ConfigRevision == 0 && current.ConfigRevision == 0 && desired.ArtifactFingerprint != current.ArtifactFingerprint
}

func artifactFencesEqual(left, right artifactInstallFence) bool {
	return left.ConfigRevision == right.ConfigRevision && left.ArtifactFingerprint == right.ArtifactFingerprint
}

func artifactInstallFencePath(modelRoot, modelDirName string) string {
	return filepath.Join(modelRoot, ".locks", modelDirLockName(modelDirName)+".json")
}

func readArtifactInstallFence(modelRoot, modelDirName string) (artifactInstallFence, bool, error) {
	data, err := os.ReadFile(artifactInstallFencePath(modelRoot, modelDirName))
	if errors.Is(err, os.ErrNotExist) {
		return artifactInstallFence{}, false, nil
	}
	if err != nil {
		return artifactInstallFence{}, false, err
	}
	var fence artifactInstallFence
	if err := json.Unmarshal(data, &fence); err != nil {
		return artifactInstallFence{}, false, err
	}
	if fence.ArtifactFingerprint == "" {
		return artifactInstallFence{}, false, errors.New("artifact install fence fingerprint is empty")
	}
	return fence, true, nil
}

func writeArtifactInstallFence(modelRoot, modelDirName string, fence artifactInstallFence) error {
	lockDir := filepath.Join(modelRoot, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fence, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(lockDir, ".model-dir-fence-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, artifactInstallFencePath(modelRoot, modelDirName)); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func newArtifactInstallSupersededError(desired, winner artifactInstallFence) error {
	return &artifactInstallSupersededError{
		Revision:           desired.ConfigRevision,
		Fingerprint:        desired.ArtifactFingerprint,
		WinningRevision:    winner.ConfigRevision,
		WinningFingerprint: winner.ArtifactFingerprint,
	}
}

func observeArtifactInstall(observe artifactInstallObserver, observation artifactInstallObservation) {
	if observe != nil {
		observe(observation)
	}
}

func localArtifactSource(modelRoot string, artifact config.Artifact) (string, bool, error) {
	return localArtifactSourceContext(context.Background(), modelRoot, artifact)
}

func localArtifactSourceContext(ctx context.Context, modelRoot string, artifact config.Artifact) (string, bool, error) {
	filename := filepath.Base(filepath.FromSlash(artifact.Object))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		return "", false, fmt.Errorf("artifact object %q has no base filename", artifact.Object)
	}

	sourcePath := filepath.Join(modelRoot, ".locks", artifactLockName("", artifact)+".source")
	gotCRC, err := crc64ECMAFileContext(ctx, sourcePath)
	if err == nil && gotCRC == artifact.CRC64ECMA {
		return sourcePath, true, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sourcePath, false, err
	}

	legacyPath := filepath.Join(modelRoot, filename)
	legacyCRC, err := crc64ECMAFileContext(ctx, legacyPath)
	if err == nil && legacyCRC == artifact.CRC64ECMA {
		return legacyPath, true, nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return sourcePath, false, err
	}
	return sourcePath, false, nil
}

func persistArtifactSource(tmpFile, sourcePath string) error {
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		return err
	}
	return os.Rename(tmpFile, sourcePath)
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

func stageFileArtifact(ctx context.Context, sourcePath, modelDir string, artifact config.Artifact) (string, error) {
	stageDir, err := os.MkdirTemp(filepath.Dir(modelDir), ".llm-agent-artifact-stage-*")
	if err != nil {
		return "", err
	}
	stageDir = filepath.Clean(stageDir)
	ready := false
	defer func() {
		if !ready {
			_ = os.RemoveAll(stageDir)
		}
	}()

	filename := filepath.Base(filepath.FromSlash(artifact.Object))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		return "", fmt.Errorf("artifact object %q has no base filename", artifact.Object)
	}
	targetPath := filepath.Join(stageDir, filename)
	if err := linkOrCopyFileContext(ctx, sourcePath, targetPath); err != nil {
		return "", err
	}
	ready = true
	return stageDir, nil
}

func stageTarGzArtifact(ctx context.Context, sourcePath, modelRoot string) (string, error) {
	extractDir, err := os.MkdirTemp(modelRoot, ".llm-agent-artifact-extract-*")
	if err != nil {
		return "", err
	}
	extractDir = filepath.Clean(extractDir)
	ready := false
	defer func() {
		if !ready {
			_ = os.RemoveAll(extractDir)
		}
	}()

	if err := extractTarGzContext(ctx, sourcePath, extractDir); err != nil {
		return "", err
	}

	if err := flattenSingleTopLevelDirContext(ctx, extractDir); err != nil {
		return "", err
	}
	ready = true
	return extractDir, nil
}

func commitStagedArtifact(ctx context.Context, modelRoot, modelDirName, stageDir, modelDir, modelName string, desired artifactInstallFence, artifact config.Artifact, observe artifactInstallObserver) (bool, error) {
	observeArtifactInstall(observe, artifactInstallObservation{
		Event:       "artifact_model_dir_lock_wait",
		Fingerprint: desired.ArtifactFingerprint,
	})
	lockFile, err := acquireModelDirLock(ctx, modelRoot, modelDirName)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = unlockArtifactFile(lockFile)
		_ = lockFile.Close()
	}()

	winner, exists, err := readArtifactInstallFence(modelRoot, modelDirName)
	if err != nil {
		return false, err
	}
	if !exists || !artifactFencesEqual(winner, desired) {
		observeArtifactInstall(observe, artifactInstallObservation{
			Event:              "artifact_install_stale_fence",
			Fingerprint:        desired.ArtifactFingerprint,
			WinningFingerprint: winner.ArtifactFingerprint,
		})
		return false, newArtifactInstallSupersededError(desired, winner)
	}

	matches, err := MarkerMatches(modelDir, modelName, artifact)
	if err != nil {
		return false, err
	}
	if matches {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := writeMarker(stageDir, modelDir, modelName, artifact); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := replaceDir(stageDir, modelDir); err != nil {
		return false, err
	}
	observeArtifactInstall(observe, artifactInstallObservation{
		Event:       "artifact_install_commit",
		Fingerprint: desired.ArtifactFingerprint,
	})
	return true, nil
}

func linkOrCopyFileContext(ctx context.Context, sourcePath, targetPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(sourcePath, targetPath); err == nil {
		return ctx.Err()
	}

	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, &contextReader{ctx: ctx, reader: in}); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func flattenSingleTopLevelDirContext(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(rootPath, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return os.Remove(rootPath)
}

func extractTarGzContext(ctx context.Context, archivePath, destDir string) error {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := extractTarEntryContext(ctx, tr, header, destDir); err != nil {
			return err
		}
	}
}

func extractTarEntryContext(ctx context.Context, reader io.Reader, header *tar.Header, destDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if _, err := io.Copy(out, &contextReader{ctx: ctx, reader: reader}); err != nil {
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
