package store

import "io/fs"

func fsSub() (fs.FS, error) { return fs.Sub(migrations, "migrations") }
