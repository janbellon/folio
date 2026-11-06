package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var content embed.FS

func Static() fs.FS {
	sub, err := fs.Sub(content, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
