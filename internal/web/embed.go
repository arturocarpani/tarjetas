package web

import (
	"embed"
	"net/http"
	"path/filepath"
)

//go:embed templates
var content embed.FS

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

func ServeStatic(w http.ResponseWriter, staticPath string) error {
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
	_, err = w.Write(staticContent)
	return err
}
