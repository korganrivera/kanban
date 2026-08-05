package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var assets embed.FS

func Handler() http.Handler {
	content, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(content))
}
