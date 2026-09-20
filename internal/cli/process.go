package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

type process struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
}

func (a *App) startProcess(name, directory string, environment []string, args ...string) (*process, error) {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = a.Out
	command.Stderr = a.Err
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	child := &process{command: command, done: make(chan struct{})}
	go func() {
		child.err = command.Wait()
		close(child.done)
	}()
	return child, nil
}

func (p *process) stop() error {
	if p == nil {
		return nil
	}
	select {
	case <-p.done:
		return nil
	default:
	}
	var err error
	if runtime.GOOS == "windows" {
		err = p.command.Process.Kill()
	} else {
		err = p.command.Process.Signal(syscall.SIGTERM)
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
		if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-p.done
		return nil
	}
}

func (p *process) failure(name string) error {
	if p.err != nil {
		return fmt.Errorf("%s exited: %w", name, p.err)
	}
	return fmt.Errorf("%s exited unexpectedly", name)
}

func waitForHTTP(ctx context.Context, url string, child *process) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: time.Second}
	var lastError error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for %s: %w", url, errors.Join(ctx.Err(), lastError))
		case <-child.done:
			return child.failure("process")
		default:
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			_, readError := io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
			closeError := response.Body.Close()
			if response.StatusCode == http.StatusOK && readError == nil && closeError == nil {
				return nil
			}
			err = errors.Join(fmt.Errorf("HTTP %d", response.StatusCode), readError, closeError)
		}
		lastError = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func checkPortsAvailable(ports ...int) (result error) {
	var listeners []net.Listener
	defer func() {
		for _, listener := range listeners {
			result = errors.Join(result, listener.Close())
		}
	}()
	for _, port := range ports {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return fmt.Errorf("development port %d is unavailable: %w", port, err)
		}
		listeners = append(listeners, listener)
	}
	return nil
}
