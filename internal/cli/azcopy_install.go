package cli

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const azCopyVersion = "10.32.8"

type azCopyRelease struct {
	Name   string
	URL    string
	SHA256 string
}

func azCopyArtifact(osName, architecture string) (azCopyRelease, error) {
	hashes := map[string]string{
		"linux-amd64":   "a95277dbc265912cefdddbaf251aa99ec648cb18ba657e8788066357a9022dc3",
		"darwin-amd64":  "2caddd8ca13cea744847929428f5f4777be1b5ca2c872e368a6158ff8b8d97df",
		"darwin-arm64":  "d17c2a7df11425f2dbc9df397af41495b32a38009e1a230e3c21892cc7a2c8c1",
		"windows-amd64": "99fa0387e91250b0aa4f3e6186e3ea07ec21b62e95e42a58da10144ad5bbf38d",
	}
	hash, ok := hashes[osName+"-"+architecture]
	if !ok {
		return azCopyRelease{}, fmt.Errorf("automatic Azure publishing tools are unavailable for %s/%s; configure HEX_AZCOPY_PATH", osName, architecture)
	}
	extension := ".zip"
	if osName == "linux" {
		extension = ".tar.gz"
	}
	name := "azcopy_" + osName + "_" + architecture + "_" + azCopyVersion + extension
	return azCopyRelease{Name: name, URL: "https://github.com/Azure/azure-storage-azcopy/releases/download/v" + azCopyVersion + "/" + name, SHA256: hash}, nil
}

func (a *App) azCopyExecutable(ctx context.Context) (string, error) {
	if configured := os.Getenv("HEX_AZCOPY_PATH"); configured != "" {
		binary, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("resolve HEX_AZCOPY_PATH: %w", err)
		}
		return binary, nil
	}
	if binary, err := exec.LookPath("azcopy"); err == nil {
		return binary, nil
	} else if !errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("resolve AzCopy on PATH: %w", err)
	}
	release, err := azCopyArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate the user tool cache: %w", err)
	}
	directory := filepath.Join(cache, "hex", "tools", "azcopy", azCopyVersion, runtime.GOOS+"-"+runtime.GOARCH)
	name := "azcopy"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return a.installAzCopy(ctx, directory, name, release)
}

func (a *App) installAzCopy(ctx context.Context, directory, name string, release azCopyRelease) (string, error) {
	binary := filepath.Join(directory, name)
	if info, err := os.Lstat(binary); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("cached AzCopy must be a regular file")
		}
		return binary, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect cached AzCopy: %w", err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", fmt.Errorf("create Azure publishing tool cache: %w", err)
	}
	temporary, err := os.MkdirTemp(directory, ".download-*")
	if err != nil {
		return "", err
	}
	defer a.removeTemporary(temporary)
	fmt.Fprintln(a.Err, "Preparing Azure publishing tools (one-time download)...")
	archive := filepath.Join(temporary, release.Name)
	if err := a.downloadBinary(ctx, release.URL, archive, release.SHA256); err != nil {
		return "", fmt.Errorf("download AzCopy: %w", err)
	}
	extracted := filepath.Join(temporary, name)
	if err := extractAzCopy(archive, extracted, name); err != nil {
		return "", fmt.Errorf("unpack AzCopy: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(extracted, binary); err != nil {
		existing, existingError := binaryChecksum(binary)
		prepared, preparedError := binaryChecksum(extracted)
		if existingError == nil && preparedError == nil && existing == prepared {
			return binary, nil
		}
		return "", fmt.Errorf("install AzCopy in user cache: %w", err)
	}
	return binary, nil
}

func extractAzCopy(archive, destination, binaryName string) (result error) {
	if strings.HasSuffix(archive, ".zip") {
		reader, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, reader.Close()) }()
		expected := strings.TrimSuffix(filepath.Base(archive), ".zip") + "/" + binaryName
		for _, entry := range reader.File {
			if entry.Name != expected {
				continue
			}
			if !entry.Mode().IsRegular() || entry.UncompressedSize64 > maxBinaryBytes {
				return errors.New("AzCopy archive contains an invalid binary")
			}
			input, err := entry.Open()
			if err != nil {
				return err
			}
			copyError := writeToolBinary(input, destination)
			return errors.Join(copyError, input.Close())
		}
		return errors.New("AzCopy binary is missing from the archive")
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, gzipReader.Close()) }()
	reader := tar.NewReader(io.LimitReader(gzipReader, maxBinaryBytes+1))
	expected := strings.TrimSuffix(filepath.Base(archive), ".tar.gz") + "/" + binaryName
	for {
		entry, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("AzCopy binary is missing from the archive")
		}
		if err != nil {
			return err
		}
		if entry.Name != expected {
			continue
		}
		if entry.Typeflag != tar.TypeReg || entry.Size > maxBinaryBytes {
			return errors.New("AzCopy archive contains an invalid binary")
		}
		return writeToolBinary(reader, destination)
	}
}

func writeToolBinary(input io.Reader, destination string) error {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	count, copyError := io.Copy(output, io.LimitReader(input, maxBinaryBytes+1))
	if err := errors.Join(copyError, output.Close()); err != nil {
		return err
	}
	if count == 0 || count > maxBinaryBytes {
		return errors.New("AzCopy binary has an invalid size")
	}
	return nil
}
