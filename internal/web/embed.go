package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

//go:embed templates
var content embed.FS

// etagCache memoizes the ETag per static path. The embedded content is fixed
// for the lifetime of the binary, so a given path's hash never changes at
// runtime — and it changes automatically on the next build/deploy.
var etagCache sync.Map // path -> string

func staticETag(path string, data []byte) string {
	if v, ok := etagCache.Load(path); ok {
		return v.(string)
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	etagCache.Store(path, etag)
	return etag
}

func GetTemplates() *embed.FS {
	return &content
}

func ServeTemplate(w http.ResponseWriter, templateName string) error {
	templateContent, err := content.ReadFile("templates/" + templateName)
	if err != nil {
		return err
	}
	_, err = w.Write(templateContent)
	return err
}

func ServeStatic(w http.ResponseWriter, r *http.Request, staticPath string) error {
	staticContent, err := content.ReadFile("templates" + staticPath)
	if err != nil {
		return err
	}
	ext := filepath.Ext(staticPath)
	switch ext {
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	case ".woff", ".woff2":
		w.Header().Set("Content-Type", "font/"+ext[1:])
	case ".ttf":
		w.Header().Set("Content-Type", "font/ttf")
	case ".eot":
		w.Header().Set("Content-Type", "application/vnd.ms-fontobject")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	case ".json":
		w.Header().Set("Content-Type", "application/json")
	}
	// Revalidate with an ETag: repeat navigations get a tiny 304 instead of
	// re-downloading ~1MB of assets (the logo GIF, chart.js, fonts), while a new
	// build changes the hash so clients never serve stale assets.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", staticETag(staticPath, staticContent))
	http.ServeContent(w, r, filepath.Base(staticPath), time.Time{}, bytes.NewReader(staticContent))
	return nil
}
