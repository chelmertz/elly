package main

import (
	"os"
	"path/filepath"
)

func defaultDBPath() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "elly", "elly.db")
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		return filepath.Join(appData, "elly", "elly.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "elly.db"
	}
	return filepath.Join(home, "AppData", "Roaming", "elly", "elly.db")
}
