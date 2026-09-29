package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Before publishing, the CLI refuses content that should never be published
// and asks before unusually large publications.

// forbiddenDirectories are refused anywhere in a folder published without a
// hex.json; projects already leave them out of their build output.
var forbiddenDirectories = []string{"node_modules", ".git", ".ssh", ".aws"}

// secretPatterns match file names that look like credentials or keys. They
// are refused in every publication.
var secretPatterns = []string{
	".env", ".env.*", "*.env",
	"*.pem", "*.key", "*.pfx", "*.p12", "*.kdbx",
	"id_rsa", "id_rsa.*", "id_dsa*", "id_ecdsa*", "id_ed25519*",
	".npmrc", ".netrc", ".pgpass", "credentials",
}

// Typical apps are a few dozen files and a few megabytes, and artifacts a
// handful of files, so anything well beyond that is worth a second look.
const (
	warnFileCount = 100
	warnTotalSize = 20 << 20
	warnFileSize  = 10 << 20
	maxListed     = 10
)

func looksSecret(name string) bool {
	name = strings.ToLower(name)
	for _, pattern := range secretPatterns {
		if matched, _ := filepath.Match(pattern, name); matched {
			return true
		}
	}
	return false
}

// findForbidden lists dependency folders, repositories, credential folders
// and secret files under directory, without descending into them.
func findForbidden(directory string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if path == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		name := strings.ToLower(entry.Name())
		if entry.IsDir() {
			for _, forbidden := range forbiddenDirectories {
				if name == forbidden {
					found = append(found, filepath.ToSlash(relative)+"/")
					return filepath.SkipDir
				}
			}
			return nil
		}
		if looksSecret(name) {
			found = append(found, filepath.ToSlash(relative))
		}
		return nil
	})
	return found, err
}

// findSecrets lists the files about to be uploaded whose names look like
// credentials or keys.
func findSecrets(files []sourceFile) []string {
	var found []string
	for _, file := range files {
		if looksSecret(filepath.Base(file.Key)) {
			found = append(found, file.Key)
		}
	}
	return found
}

// sizeWarnings describes what makes a publication unusually large.
func sizeWarnings(files []sourceFile) ([]string, error) {
	var total int64
	var large []string
	for _, file := range files {
		info, err := os.Stat(file.Path)
		if err != nil {
			return nil, err
		}
		total += info.Size()
		if info.Size() > warnFileSize {
			large = append(large, fmt.Sprintf("%s (%s)", file.Key, formatSize(info.Size())))
		}
	}

	var warnings []string
	if len(files) > warnFileCount {
		warnings = append(warnings, fmt.Sprintf("%d files (more than %d)", len(files), warnFileCount))
	}
	if total > warnTotalSize {
		warnings = append(warnings, fmt.Sprintf("%s in total (more than %s)", formatSize(total), formatSize(warnTotalSize)))
	}
	if len(large) > 0 {
		warnings = append(warnings, fmt.Sprintf("files over %s: %s", formatSize(warnFileSize), listed(large)))
	}
	return warnings, nil
}

// checkPublication refuses forbidden or secret content, and asks before a
// large publication: interactively, or through --yes when there is no
// terminal to ask on.
func (a *App) checkPublication(ctx context.Context, files []sourceFile, forbidden []string, assumeYes bool) error {
	forbidden = append(forbidden, findSecrets(files)...)
	if len(forbidden) > 0 {
		return fmt.Errorf("refusing to publish content that looks private or should not be published: %s\n"+
			"Remove it, or publish a folder that contains only what you want to share", listed(forbidden))
	}

	warnings, err := sizeWarnings(files)
	if err != nil || len(warnings) == 0 {
		return err
	}
	fmt.Fprintf(a.Err, "This publication is unusually large: %s.\n", strings.Join(warnings, "; "))
	if assumeYes {
		return nil
	}
	if !a.Interactive {
		return errors.New("rerun with --yes to publish it anyway")
	}
	answer, err := a.ask(ctx, "Continue? [y/N] ")
	if err != nil {
		return err
	}
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return errors.New("publication cancelled")
	}
	return nil
}

func listed(values []string) string {
	if len(values) > maxListed {
		return strings.Join(values[:maxListed], ", ") + fmt.Sprintf(" and %d more", len(values)-maxListed)
	}
	return strings.Join(values, ", ")
}
