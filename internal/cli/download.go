package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxBinaryBytes = 128 << 20

func validateDownloadURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("invalid download URL")
	}
	validScheme := parsed.Scheme == "https" || (parsed.Scheme == "http" && isLocalHost(parsed.Hostname()))
	if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("downloads require HTTPS, or HTTP on loopback, without embedded credentials")
	}
	return nil
}

func (a *App) fetchDownload(ctx context.Context, location string) (*http.Response, error) {
	if err := validateDownloadURL(location); err != nil {
		return nil, err
	}
	client := *a.HTTP
	client.Timeout = 5 * time.Minute
	client.CheckRedirect = func(request *http.Request, previous []*http.Request) error {
		if len(previous) >= 10 {
			return errors.New("too many download redirects")
		}
		if previous[0].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("refusing an HTTPS download redirect to HTTP")
		}
		return validateDownloadURL(request.URL.String())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Hex-CLI")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download release artifact: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		closeLogged(a.Err, response.Body)
		return nil, fmt.Errorf("download release artifact returned HTTP %d", response.StatusCode)
	}
	return response, nil
}

func (a *App) downloadBytes(ctx context.Context, location string, limit int64) ([]byte, error) {
	response, err := a.fetchDownload(ctx, location)
	if err != nil {
		return nil, err
	}
	defer closeLogged(a.Err, response.Body)
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read release artifact: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release artifact exceeds %d bytes", limit)
	}
	return data, nil
}

func (a *App) downloadBinary(ctx context.Context, location, destination, expectedHash string) (result error) {
	expected, err := hex.DecodeString(expectedHash)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("release requires a valid SHA-256 checksum")
	}
	response, err := a.fetchDownload(ctx, location)
	if err != nil {
		return err
	}
	defer closeLogged(a.Err, response.Body)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if result != nil {
			a.removeTemporary(destination)
		}
	}()
	hash := sha256.New()
	count, copyError := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, maxBinaryBytes+1))
	if err := errors.Join(copyError, file.Close()); err != nil {
		return fmt.Errorf("save downloaded binary: %w", err)
	}
	if count > maxBinaryBytes {
		return errors.New("downloaded binary exceeds 128 MiB")
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash) {
		return errors.New("downloaded binary SHA-256 mismatch; nothing was installed, retry the command")
	}
	return nil
}
