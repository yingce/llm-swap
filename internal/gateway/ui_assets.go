package gateway

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
)

//go:embed admin_dist
var adminAssets embed.FS

func embeddedAdminIndex() ([]byte, bool) {
	data, err := adminAssets.ReadFile("admin_dist/index.html")
	if err != nil || bytes.Contains(data, []byte("data-llmswap-admin-placeholder")) {
		return nil, false
	}
	return data, true
}

func embeddedAdminAssetHandler() http.Handler {
	dist, err := fs.Sub(adminAssets, "admin_dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.StripPrefix("/ui/", http.FileServer(http.FS(dist)))
}
