package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*
var staticFS embed.FS

// templatesFS returns the embedded filesystem for templates.
func templatesFS() fs.FS {
	sub, _ := fs.Sub(staticFS, "templates")
	return sub
}
