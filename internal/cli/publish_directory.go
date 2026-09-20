package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func publishDirectory(project, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	if found, err := hasSiteIndex(filepath.Join(project, "dist")); err != nil || found {
		return "dist", err
	}
	hasBuild, err := hasBuildScript(project)
	if err != nil {
		return "", err
	}
	if hasBuild {
		return "", errors.New("this project declares a build script but dist/index.html is missing; run your project's build command first, or set directory in hex.json to its build output")
	}
	for _, directory := range []string{"public", "."} {
		if found, err := hasSiteIndex(filepath.Join(project, directory)); err != nil || found {
			return directory, err
		}
	}
	return "", errors.New("no site found: expected index.html in dist/, public/, or the project root; build your app or set directory in hex.json")
}

func hasSiteIndex(directory string) (bool, error) {
	path := filepath.Join(directory, "index.html")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect publish entry point %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("publish entry point %q must be a regular file, not a directory or symlink", path)
	}
	return true, nil
}

func hasBuildScript(project string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(project, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read package.json to determine whether a build is needed: %w", err)
	}
	var manifest struct {
		Scripts struct {
			Build string `json:"build"`
		} `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("parse package.json to determine whether a build is needed: %w", err)
	}
	return strings.TrimSpace(manifest.Scripts.Build) != "", nil
}

func rootProjectFile(name string) bool {
	switch strings.ToLower(name) {
	case "hex.json", "hex.dev.json", "agents.md", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb":
		return true
	default:
		return false
	}
}
