package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func (a *App) azureLogin(ctx context.Context, binary string) error {
	fmt.Fprintln(a.Err, "Sign in to Azure Storage to continue. Complete the Microsoft sign-in using the code shown below.")
	if a.Interactive {
		if err := a.OpenBrowser("https://microsoft.com/devicelogin"); err != nil {
			fmt.Fprintln(a.Err, "Open https://microsoft.com/devicelogin in your browser:", err)
		}
	}
	args := []string{"login"}
	if tenant := os.Getenv("AZCOPY_TENANT_ID"); tenant != "" {
		args = append(args, "--tenant-id="+tenant)
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = a.Dir
	command.Stdin = a.In
	command.Stdout = a.Err
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("Azure Storage sign-in failed: %w", err)
	}
	return nil
}

func (a *App) ensureAzureLogin(ctx context.Context, binary string) error {
	if os.Getenv("HEX_PUBLISH_SAS") != "" || os.Getenv("AZCOPY_AUTO_LOGIN_TYPE") != "" {
		return nil
	}
	statusContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	status := exec.CommandContext(statusContext, binary, "login", "status")
	status.Dir = a.Dir
	if err := status.Run(); err == nil {
		return nil
	} else {
		if statusContext.Err() != nil {
			return fmt.Errorf("check Azure Storage sign-in: %w", statusContext.Err())
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("check Azure Storage sign-in: %w", err)
		}
	}
	if !a.Interactive {
		return errors.New("Azure Storage sign-in is required; rerun this Hex command in an interactive terminal to sign in automatically, or configure HEX_PUBLISH_SAS or AZCOPY_AUTO_LOGIN_TYPE for automation")
	}
	return a.azureLogin(ctx, binary)
}

func (a *App) storageCommand(ctx context.Context, args ...string) error {
	binary, err := a.azCopyExecutable(ctx)
	if err != nil {
		return err
	}
	if err := a.ensureAzureLogin(ctx, binary); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = a.Dir
	command.Stdin = a.In
	command.Stdout = a.Err
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("Azure publishing failed; check the storage permissions, network/VPN connection, and provider output above: %w", err)
	}
	return nil
}
