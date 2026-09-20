package hex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"text/template"
)

const defaultCLIReleaseURL = "https://github.com/crazycatviking/hex/releases/latest/download"

func (s *Server) cliReleaseURL() (*url.URL, error) {
	value := s.config.CLIReleaseURL
	if value == "" {
		value = defaultCLIReleaseURL
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid CLI release URL")
	}
	validScheme := parsed.Scheme == "https" || (parsed.Scheme == "http" && loopbackHost(parsed.Hostname()))
	if !validScheme || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("CLI release URL requires HTTPS, or HTTP on loopback, without credentials or query parameters")
	}
	return parsed, nil
}

func (s *Server) installer(w http.ResponseWriter, r *http.Request) {
	osName := r.PathValue("os")
	templateName := "unix.sh"
	extension := "sh"
	expectedOS := ""
	switch osName {
	case "macos":
		expectedOS = "Darwin"
	case "linux":
		expectedOS = "Linux"
	case "windows":
		templateName = "windows.ps1"
		extension = "ps1"
	default:
		http.NotFound(w, r)
		return
	}
	document, err := s.connectionSettings()
	if err != nil {
		writeServerError(w, err)
		return
	}
	releaseURL, err := s.cliReleaseURL()
	if err != nil {
		writeServerError(w, err)
		return
	}
	connection, err := json.Marshal(document)
	if err != nil || len(connection) > 64*1024 {
		writeServerError(w, fmt.Errorf("platform connection settings cannot be encoded within 64 KiB"))
		return
	}
	content, err := platformAssets.ReadFile("installers/" + templateName)
	if err != nil {
		writeServerError(w, err)
		return
	}
	script, err := template.New(templateName).Parse(string(content))
	if err != nil {
		writeServerError(w, err)
		return
	}
	protocols := "=https"
	if releaseURL.Scheme == "http" {
		protocols = "=http,https"
	}
	var output bytes.Buffer
	err = script.Execute(&output, struct {
		Connection string
		ReleaseURL string
		ExpectedOS string
		Protocols  string
	}{
		Connection: base64.StdEncoding.EncodeToString(connection),
		ReleaseURL: base64.StdEncoding.EncodeToString([]byte(strings.TrimRight(releaseURL.String(), "/"))),
		ExpectedOS: expectedOS,
		Protocols:  protocols,
	})
	if err != nil {
		writeServerError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="install-hex-%s.%s"`, osName, extension))
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(output.Bytes()); err != nil {
		slog.Error("write installer", "error", err)
	}
}
