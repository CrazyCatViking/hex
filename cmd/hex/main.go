package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/crazycatviking/hex/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := cli.New(os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		err = app.Execute(ctx, os.Args[1:], version)
	}
	if err == nil {
		return
	}

	code := 1
	var exit *cli.ExitError
	if errors.As(err, &exit) {
		code = exit.Code
	}
	if err.Error() != "" {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
