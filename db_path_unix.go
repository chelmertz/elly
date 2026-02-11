//go:build !darwin && !windows

package main

import (
	"os"
	"path/filepath"
)

func defaultDBPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "elly", "elly.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "elly.db"
	}
	return filepath.Join(home, ".local", "share", "elly", "elly.db")
}
