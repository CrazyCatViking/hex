package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

func (a *App) removeTemporary(directory string) {
	if err := os.RemoveAll(directory); err != nil {
		fmt.Fprintln(a.Err, "Remove temporary directory:", err)
	}
}

func (a *App) manageServices(ctx context.Context, settings devSettings, directory, action string) error {
	content, err := assets.ReadFile("assets/compose.yaml")
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(path, content, 0600); err != nil {
		return err
	}
	if err := a.external(ctx, settings.ServerDirectory, composeEnvironment(settings), "docker", composeArguments(settings, path, action)...); err != nil {
		return fmt.Errorf("Docker Compose %s failed; a running Docker engine and Compose v2 are required: %w", action, err)
	}
	return nil
}

func (a *App) runDevelopment(ctx context.Context, settings devSettings) (result error) {
	if err := checkPortsAvailable(settings.Port, settings.APIPort); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "hex-dev-*")
	if err != nil {
		return err
	}
	defer a.removeTemporary(temporary)
	if err := os.MkdirAll(filepath.Join(settings.DataDirectory, "sites", "public", "sites"), 0755); err != nil {
		return err
	}
	environment := serverEnvironment(settings)
	binary := settings.Binary
	if binary == "" {
		binary = filepath.Join(temporary, "hex-server")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		fmt.Fprintf(a.Out, "Building %s in %s\n", settings.Package, settings.ServerDirectory)
		if err := a.external(ctx, settings.ServerDirectory, environment, "go", "build", "-o", binary, settings.Package); err != nil {
			return err
		}
	}
	if len(settings.Services) > 0 {
		fmt.Fprintln(a.Out, "Starting local services:", strings.Join(settings.Services, ", "))
		if err := a.manageServices(ctx, settings, temporary, "up"); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	server, err := a.startProcess(binary, settings.ServerDirectory, environment, settings.Args...)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, server.stop()) }()
	if err := waitForHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/api/hex/capabilities", settings.APIPort), server); err != nil {
		return err
	}
	nginx, err := a.startNginx(ctx, settings, temporary)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, nginx.stop()) }()
	if err := waitForHTTP(ctx, fmt.Sprintf("http://127.0.0.1:%d/healthz", settings.Port), nginx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Hex development environment ready: http://localhost:%d\n", settings.Port)
	fmt.Fprintf(a.Out, "Sites: http://<name>.localhost:%d/\n", settings.Port)
	fmt.Fprintln(a.Out, "Publishing root:", filepath.Join(settings.DataDirectory, "sites", "public", "sites"))
	fmt.Fprintln(a.Out, "Local gateway and API are loopback-only and unauthenticated.")
	if len(settings.Services) > 0 {
		fmt.Fprintln(a.Out, "Service containers persist after exit. Use hex dev --stop-services to stop them.")
	}
	select {
	case <-ctx.Done():
		return nil
	case <-server.done:
		return server.failure("server")
	case <-nginx.done:
		return nginx.failure("NGINX")
	}
}

func nginxQuote(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n\x00$") {
		return "", errors.New("unsupported NGINX configuration path")
	}
	return strconv.Quote(filepath.ToSlash(value)), nil
}

func (a *App) startNginx(ctx context.Context, settings devSettings, directory string) (*process, error) {
	binary := os.Getenv("NGINX_BIN")
	if binary == "" {
		binary = "nginx"
	}
	version, err := exec.CommandContext(ctx, binary, "-V").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("NGINX is required; install it or set NGINX_BIN: %w", err)
	}
	mimeTypes := os.Getenv("NGINX_MIME_TYPES")
	if mimeTypes == "" {
		prefix := "/usr/local/nginx"
		if match := regexp.MustCompile(`--prefix=([^\s]+)`).FindStringSubmatch(string(version)); match != nil {
			prefix = match[1]
		}
		configPath := filepath.Join(prefix, "conf", "nginx.conf")
		if match := regexp.MustCompile(`--conf-path=([^\s]+)`).FindStringSubmatch(string(version)); match != nil {
			configPath = match[1]
		}
		mimeTypes = filepath.Join(filepath.Dir(configPath), "mime.types")
	}
	template, err := assets.ReadFile("assets/nginx.conf.template")
	if err != nil {
		return nil, err
	}
	pid, err := nginxQuote(filepath.Join(directory, "nginx.pid"))
	if err != nil {
		return nil, err
	}
	mimePath, err := nginxQuote(mimeTypes)
	if err != nil {
		return nil, err
	}
	siteRoot, err := nginxQuote(filepath.Join(settings.DataDirectory, "sites", "public", "sites", "__HEX_SITE__"))
	if err != nil {
		return nil, err
	}
	siteRoot = strings.ReplaceAll(siteRoot, "__HEX_SITE__", "$hex_site")
	replacements := strings.NewReplacer(
		"user nginx;", "",
		"${HEX_SITE_DOMAIN_PATTERN}", "localhost",
		"include /etc/nginx/mime.types;", "include "+mimePath+";",
		"listen 8080;", fmt.Sprintf("listen 127.0.0.1:%d;", settings.Port),
		"http://127.0.0.1:8081", fmt.Sprintf("http://127.0.0.1:%d", settings.APIPort),
		"root /mnt/sites/public/sites/$hex_site;", "root "+siteRoot+";",
		"http {", `http {
    access_log off;
    client_body_temp_path client-body;
    proxy_temp_path proxy;
    fastcgi_temp_path fastcgi;
    scgi_temp_path scgi;
    uwsgi_temp_path uwsgi;`,
	)
	config := "daemon off;\npid " + pid + ";\nerror_log stderr;\nlock_file logs/nginx.lock;\n" + replacements.Replace(string(template))
	if err := os.MkdirAll(filepath.Join(directory, "logs"), 0755); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "nginx.conf")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		return nil, err
	}
	args := []string{"-p", directory + string(filepath.Separator), "-c", path}
	if err := a.external(ctx, a.Dir, nil, binary, append([]string{"-t"}, args...)...); err != nil {
		return nil, err
	}
	return a.startProcess(binary, a.Dir, nil, args...)
}
