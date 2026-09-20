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
	"time"
)

func azureCLIEnvironment() []string {
	return append(os.Environ(),
		"AZURE_CORE_ENABLE_BROKER_ON_WINDOWS=false",
		"AZURE_CORE_LOGIN_EXPERIENCE_V2=off",
	)
}

func azureCLICommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	binary, err := exec.LookPath("az")
	if err != nil {
		return nil, errors.New("Azure CLI is required for Entra browser sign-in; install it from https://aka.ms/installazurecliwindows (Windows) or https://learn.microsoft.com/cli/azure/install-azure-cli, then rerun the Hex command")
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

func azureTenantArguments(args []string) ([]string, error) {
	if tenant := os.Getenv("AZCOPY_TENANT_ID"); tenant != "" {
		if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(tenant) {
			return nil, errors.New("AZCOPY_TENANT_ID must be a tenant ID or domain name")
		}
		args = append(args, "--tenant", tenant)
	}
	return args, nil
}

func (a *App) azureLogin(ctx context.Context) error {
	if os.Getenv("CODESPACES") == "true" || (runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "") {
		return errors.New("Entra browser sign-in requires a desktop browser; run this Hex command in a desktop terminal, or use configured automation credentials. Device-code fallback is not supported")
	}
	args, err := azureTenantArguments([]string{"login", "--allow-no-subscriptions", "--scope", "https://storage.azure.com/.default", "--output", "none"})
	if err != nil {
		return err
	}
	command, err := azureCLICommand(ctx, args...)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Err, "Sign in to Azure Storage in the browser opened by Azure CLI. Complete your organization's MFA prompts to continue.")
	command.Dir = a.Dir
	command.Stdin = a.In
	command.Stdout = a.Err
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("Entra browser sign-in failed: %w", err)
	}
	return nil
}

func (a *App) ensureAzureLogin(ctx context.Context) (bool, error) {
	mode := strings.ToUpper(os.Getenv("AZCOPY_AUTO_LOGIN_TYPE"))
	if os.Getenv("HEX_PUBLISH_SAS") != "" {
		return false, nil
	}
	if mode == "DEVICE" {
		return false, errors.New("device-code authentication is not supported; remove AZCOPY_AUTO_LOGIN_TYPE=DEVICE to use Entra browser sign-in")
	}
	if mode != "" && mode != "AZCLI" {
		return false, nil
	}
	args, err := azureTenantArguments([]string{"account", "get-access-token", "--resource", "https://storage.azure.com/", "--output", "none"})
	if err != nil {
		return false, err
	}
	statusContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status, err := azureCLICommand(statusContext, args...)
	if err != nil {
		return false, err
	}
	status.Dir = a.Dir
	err = status.Run()
	if err == nil {
		return true, nil
	}
	if statusContext.Err() != nil {
		return false, fmt.Errorf("check Azure CLI storage session: %w", statusContext.Err())
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false, fmt.Errorf("check Azure CLI storage session: %w", err)
	}
	if !a.Interactive {
		return false, errors.New("Entra browser sign-in is required; rerun this Hex command in an interactive desktop terminal, or configure automation credentials")
	}
	return true, a.azureLogin(ctx)
}

func (a *App) storageCommand(ctx context.Context, args ...string) error {
	useAzureCLI, err := a.ensureAzureLogin(ctx)
	if err != nil {
		return err
	}
	binary, err := a.azCopyExecutable(ctx)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = a.Dir
	if useAzureCLI {
		command.Env = append(azureCLIEnvironment(), "AZCOPY_AUTO_LOGIN_TYPE=AZCLI")
	}
	command.Stdin = a.In
	command.Stdout = a.Err
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("Azure publishing failed; check the storage permissions, network/VPN connection, and provider output above: %w", err)
	}
	return nil
}
