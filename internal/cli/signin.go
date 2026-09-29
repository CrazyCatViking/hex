package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// The CLI signs in to the platform API itself, as the public-client app
// registration the platform advertises, so employees need no other tools.
// The first sign-in opens the browser; later commands reuse and silently
// refresh the saved session.

// tokenCacheFile persists the sign-in session in the Hex configuration
// directory, readable only by the user, like Azure CLI's own cache.
type tokenCacheFile struct {
	path string
}

func signInCache() (tokenCacheFile, error) {
	directory, err := profileDirectory()
	if err != nil {
		return tokenCacheFile{}, err
	}
	return tokenCacheFile{path: filepath.Join(directory, "sign-in.json")}, nil
}

func (c tokenCacheFile) Replace(ctx context.Context, cache cache.Unmarshaler, hints cache.ReplaceHints) error {
	data, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the saved sign-in: %w", err)
	}
	// A damaged cache only costs a new browser sign-in, so it is not an error.
	if err := cache.Unmarshal(data); err != nil {
		return nil
	}
	return nil
}

func (c tokenCacheFile) Export(ctx context.Context, cache cache.Marshaler, hints cache.ExportHints) error {
	data, err := cache.Marshal()
	if err != nil {
		return err
	}
	if err := atomicWrite(c.path, data, 0600); err != nil {
		return fmt.Errorf("save the sign-in: %w", err)
	}
	return nil
}

func (a *App) signInClient(project Project) (public.Client, error) {
	tokenCache, err := signInCache()
	if err != nil {
		return public.Client{}, err
	}
	host := "https://login.microsoftonline.com"
	options := []public.Option{public.WithCache(tokenCache)}
	if a.signInHost != "" {
		host = a.signInHost
		options = append(options, public.WithInstanceDiscovery(false), public.WithHTTPClient(a.signInHTTP))
	}
	options = append(options, public.WithAuthority(host+"/"+project.TenantID))
	return public.New(project.ClientID, options...)
}

func signInScopes(resource string) []string {
	return []string{strings.TrimRight(resource, "/") + "/.default"}
}

// signedInToken returns a token from the saved session, signing in through
// the browser when there is none and the terminal is interactive.
func (a *App) signedInToken(ctx context.Context, project Project) (string, error) {
	client, err := a.signInClient(project)
	if err != nil {
		return "", err
	}

	if token, ok := silentToken(ctx, client, project.Resource); ok {
		return token, nil
	}
	if !a.Interactive {
		return "", errors.New("not signed in to the platform; run hex login in a terminal, or set HEX_TOKEN")
	}

	result, err := a.browserSignIn(ctx, client, project.Resource)
	if err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func silentToken(ctx context.Context, client public.Client, resource string) (string, bool) {
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return "", false
	}
	for _, account := range accounts {
		result, err := client.AcquireTokenSilent(ctx, signInScopes(resource), public.WithSilentAccount(account))
		if err == nil {
			return result.AccessToken, true
		}
	}
	return "", false
}

func (a *App) browserSignIn(ctx context.Context, client public.Client, resource string) (public.AuthResult, error) {
	if os.Getenv("CODESPACES") == "true" || (runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "") {
		return public.AuthResult{}, errors.New("signing in requires a desktop browser; run hex login in a desktop terminal, or set HEX_TOKEN. Device-code sign-in is not supported")
	}

	fmt.Fprintln(a.Err, "Sign in to the platform in your browser. Complete your organization's MFA prompts to continue.")
	result, err := client.AcquireTokenInteractive(ctx, signInScopes(resource),
		public.WithRedirectURI("http://localhost"),
		public.WithOpenURL(a.OpenBrowser),
	)
	if err != nil {
		return public.AuthResult{}, fmt.Errorf("sign-in failed: %w", err)
	}
	return result, nil
}

// signIn always opens the browser, for hex login.
func (a *App) signIn(ctx context.Context, project Project) error {
	client, err := a.signInClient(project)
	if err != nil {
		return err
	}
	result, err := a.browserSignIn(ctx, client, project.Resource)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Signed in as %s.\n", result.Account.PreferredUsername)
	return nil
}

// signOut forgets the saved session for the platform's sign-in app.
func (a *App) signOut(ctx context.Context, project Project) error {
	client, err := a.signInClient(project)
	if err != nil {
		return err
	}
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if err := client.RemoveAccount(ctx, account); err != nil {
			return fmt.Errorf("remove the saved sign-in: %w", err)
		}
	}
	fmt.Fprintln(a.Out, "Signed out.")
	return nil
}
