package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpLoginStatus struct {
	LoginID   string `json:"loginID"`
	Status    string `json:"status"`
	SignInURL string `json:"signInURL,omitempty"`
	Message   string `json:"message"`
}

type mcpLoginJob struct {
	input  mcpProjectInput
	status mcpLoginStatus
	ready  chan struct{}
	done   chan struct{}
	once   sync.Once
}

type mcpLogins struct {
	mu  sync.Mutex
	job *mcpLoginJob
}

func (logins *mcpLogins) snapshot(job *mcpLoginJob) mcpLoginStatus {
	logins.mu.Lock()
	defer logins.mu.Unlock()
	return job.status
}

func (logins *mcpLogins) start(serverContext, requestContext context.Context, app *App, version string, input mcpProjectInput) (mcpLoginStatus, error) {
	child, err := app.mcpApp(serverContext, input.ProjectPath)
	if err != nil {
		return mcpLoginStatus{}, err
	}
	logins.mu.Lock()
	if previous := logins.job; previous != nil {
		select {
		case <-previous.done:
		default:
			if previous.input != input {
				logins.mu.Unlock()
				return mcpLoginStatus{}, errors.New("a sign-in for another project or account is already in progress; finish it before starting another")
			}
			status := previous.status
			logins.mu.Unlock()
			return status, nil
		}
	}
	job := &mcpLoginJob{
		input:  input,
		status: mcpLoginStatus{LoginID: rand.Text(), Status: "starting", Message: "Starting company sign-in. Call hex_login_status to retrieve the browser link and completion status."},
		ready:  make(chan struct{}), done: make(chan struct{}),
	}
	logins.job = job
	logins.mu.Unlock()
	go logins.run(serverContext, child, version, job)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-job.ready:
	case <-timer.C:
	case <-requestContext.Done():
		return mcpLoginStatus{}, requestContext.Err()
	}
	return logins.snapshot(job), nil
}

func (logins *mcpLogins) run(ctx context.Context, child *App, version string, job *mcpLoginJob) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var output, diagnostics bytes.Buffer
	child.Out, child.Err = &output, &diagnostics
	child.browserHandoff = true
	openBrowser := child.OpenBrowser
	child.OpenBrowser = func(address string) error {
		logins.mu.Lock()
		job.status.Status = "waiting_for_browser"
		job.status.SignInURL = address
		job.status.Message = "Complete company sign-in and MFA in your browser, then return to the assistant. If no browser opened, use this sign-in link on the machine running Hex. Never paste passwords or tokens into chat."
		logins.mu.Unlock()
		job.once.Do(func() { close(job.ready) })
		if err := openBrowser(address); err != nil {
			logins.mu.Lock()
			job.status.Message = "Open the sign-in link on the machine running Hex, complete company sign-in and MFA, then return to the assistant. The browser could not be opened automatically."
			logins.mu.Unlock()
		}
		return nil // The assistant can show the link even without a GUI opener.
	}
	args := append([]string{"login"}, job.input.flags()...)
	err := child.Execute(ctx, args, version)
	logins.mu.Lock()
	job.status.SignInURL = ""
	job.status.Status = "signed_in"
	job.status.Message = strings.TrimSpace(output.String())
	if err != nil {
		job.status.Status = "failed"
		job.status.Message = err.Error()
	}
	close(job.done)
	logins.mu.Unlock()
	job.once.Do(func() { close(job.ready) })
}

func (a *App) addMCPLoginTools(ctx context.Context, server *mcp.Server, version string) {
	logins := &mcpLogins{}
	mcp.AddTool(server, &mcp.Tool{Name: "hex_login", Description: "Start company browser sign-in for Hex from this harness. Returns quickly with a loginID and, when ready, a browser link. The user completes sign-in/MFA in the browser; credentials stay in Hex's saved session. Use when a platform operation requests hex login."},
		func(requestContext context.Context, request *mcp.CallToolRequest, input mcpProjectInput) (*mcp.CallToolResult, mcpLoginStatus, error) {
			status, err := logins.start(ctx, requestContext, a, version, input)
			return nil, status, err
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hex_login_status", Description: "Check the browser link or completion of a Hex login. After showing the link, let the user finish sign-in before checking again. Retry the original operation once signed in.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(requestContext context.Context, request *mcp.CallToolRequest, input struct {
			LoginID string `json:"loginID" jsonschema:"The loginID returned by hex_login"`
		}) (*mcp.CallToolResult, mcpLoginStatus, error) {
			logins.mu.Lock()
			defer logins.mu.Unlock()
			if logins.job == nil || logins.job.status.LoginID != input.LoginID {
				return nil, mcpLoginStatus{}, fmt.Errorf("unknown loginID; start a new sign-in with hex_login")
			}
			return nil, logins.job.status, nil
		})
}
