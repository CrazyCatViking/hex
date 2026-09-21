package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type App struct {
	Dir         string
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
	HTTP        *http.Client
	OpenBrowser func(string) error
	input       *bufio.Reader
	writes      *writeState
}

type writeState struct {
	mu  sync.Mutex
	err error
}

type checkedWriter struct {
	target io.Writer
	state  *writeState
}

func (w checkedWriter) Write(data []byte) (int, error) {
	w.state.mu.Lock()
	defer w.state.mu.Unlock()
	count, err := w.target.Write(data)
	if err != nil && w.state.err == nil {
		w.state.err = err
	}
	return count, err
}

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return ""
}

func New(in io.Reader, out, stderr io.Writer) (*App, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	interactive := false
	if input, ok := in.(*os.File); ok {
		if output, ok := out.(*os.File); ok {
			interactive = term.IsTerminal(int(input.Fd())) && term.IsTerminal(int(output.Fd()))
		}
	}

	writes := &writeState{}
	return &App{
		Dir:         directory,
		In:          in,
		Out:         checkedWriter{target: out, state: writes},
		Err:         checkedWriter{target: stderr, state: writes},
		Interactive: interactive,
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		OpenBrowser: browser.OpenURL,
		input:       bufio.NewReader(in),
		writes:      writes,
	}, nil
}

func (a *App) Execute(ctx context.Context, args []string, version string) error {
	root := &cobra.Command{
		Use:           "hex",
		Short:         "Build, connect and publish Hex sites",
		Version:       version,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	root.SetArgs(args)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(a.setupCommand(), a.initCommand(), a.devCommand())
	root.AddCommand(a.updateCommand())
	root.AddCommand(a.publishCommand(), a.deleteCommand(), a.loginCommand())
	root.AddCommand(a.sitesCommand(), a.capabilitiesCommand(), a.skillsCommand())
	root.AddCommand(a.accessCommand(), a.whoamiCommand())
	err := root.ExecuteContext(ctx)
	a.writes.mu.Lock()
	defer a.writes.mu.Unlock()
	return errors.Join(err, a.writes.err)
}

func (a *App) printJSON(value any) error {
	encoder := json.NewEncoder(a.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (a *App) ask(ctx context.Context, question string) (string, error) {
	if _, err := fmt.Fprint(a.Out, question); err != nil {
		return "", err
	}
	type answer struct {
		line string
		err  error
	}
	answers := make(chan answer, 1)
	go func() {
		line, err := a.input.ReadString('\n')
		answers <- answer{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-answers:
		if result.err != nil && result.line == "" {
			return "", fmt.Errorf("read terminal input: %w", result.err)
		}
		return result.line, nil
	}
}

func (a *App) external(ctx context.Context, directory string, environment []string, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = environment
	command.Stdin = a.In
	command.Stdout = a.Out
	command.Stderr = a.Err
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", name, err)
	}
	return nil
}

func resolvePath(directory, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(directory, path)
}
