package gateway

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

const installWorkerScriptPath = "/usr/local/share/llmswap/install-worker.sh"

func (s *Server) handleInstallWorkerScript(w http.ResponseWriter, r *http.Request) {
	data, err := readInstallWorkerScript()
	if err != nil {
		http.Error(w, "install worker script is not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="install-worker.sh"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func readInstallWorkerScript() ([]byte, error) {
	if path := os.Getenv("LLMSWAP_INSTALL_WORKER_SCRIPT_PATH"); path != "" {
		return os.ReadFile(path)
	}
	if data, err := os.ReadFile(installWorkerScriptPath); err == nil {
		return data, nil
	}
	if path, ok := findInstallWorkerScriptFromWorkingDir(); ok {
		return os.ReadFile(path)
	}
	return nil, errors.New("install-worker.sh not found")
}

func findInstallWorkerScriptFromWorkingDir() (string, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(wd, "scripts", "install-worker.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", false
		}
		wd = parent
	}
}
