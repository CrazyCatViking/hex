package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

var tenantPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]{0,252}$`)

func azureCLIEnvironment() []string {
	return append(os.Environ(),
		"AZURE_CORE_ENABLE_BROKER_ON_WINDOWS=false",
		"AZURE_CORE_LOGIN_EXPERIENCE_V2=off",
	)
}

func azureCLICommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	binary, err := exec.LookPath("az")
	if err != nil {
		return nil, errors.New("Azure CLI is required for Entra sign-in; install it from https://aka.ms/installazurecliwindows (Windows) or https://learn.microsoft.com/cli/azure/install-azure-cli, then rerun the Hex command")
	}
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.CommandContext(ctx, "cmd.exe", append([]string{"/d", "/c", "az"}, args...)...)
	} else {
		command = exec.CommandContext(ctx, binary, args...)
	}
	command.Env = azureCLIEnvironment()
	return command, nil
}

// validateResource rejects API resources that are not plain identifiers.
// Resources can come from a project's hex.json or a flag, and on Windows
// Azure CLI runs through cmd.exe, which would interpret shell characters.
func validateResource(resource string) error {
	if !apiResourcePattern.MatchString(resource) {
		return errors.New("invalid API resource identifier; use a value such as api://<client id>")
	}
	return nil
}

// azureTenantArguments pins Azure CLI to HEX_TENANT_ID when set, for people
// whose default Azure CLI tenant is not the platform's.
func azureTenantArguments(args []string) ([]string, error) {
	if tenant := os.Getenv("HEX_TENANT_ID"); tenant != "" {
		if !tenantPattern.MatchString(tenant) {
			return nil, errors.New("HEX_TENANT_ID must be a tenant ID or domain name")
		}
		args = append(args, "--tenant", tenant)
	}
	return args, nil
}

// azureLogin signs in with Azure CLI for the platform's API resource.
func (a *App) azureLogin(ctx context.Context, resource string) error {
	if err := validateResource(resource); err != nil {
		return err
	}
	if os.Getenv("CODESPACES") == "true" || (runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		return errors.New("Entra browser sign-in requires a desktop browser; run hex login in a desktop terminal, or set HEX_TOKEN. Device-code sign-in is not supported")
	}
	scope := strings.TrimRight(resource, "/") + "/.default"
	args, err := azureTenantArguments([]string{"login", "--allow-no-subscriptions", "--scope", scope, "--output", "none"})
	if err != nil {
		return err
	}
	command, err := azureCLICommand(ctx, args...)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Err, "Sign in to the platform in the browser opened by Azure CLI. Complete your organization's MFA prompts to continue.")
	command.Dir = a.Dir
	command.Stdin = a.In
	command.Stdout = a.Err
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("Entra browser sign-in failed: %w", err)
	}
	return nil
}
