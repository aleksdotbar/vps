package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/hostsetup"
)

// `panel install` drives on-host installation; the other branches are detached
// runners and the panel-user steps the installer itself launches.
func install(arguments []string, output io.Writer) error {
	if len(arguments) == 0 || arguments[0] == "run" || (len(arguments[0]) > 0 && arguments[0][0] == '-') {
		if len(arguments) > 0 && arguments[0] == "run" {
			arguments = arguments[1:]
		}
		return installRun(arguments, output)
	}
	switch arguments[0] {
	case "panel-bootstrap", "panel-proof":
		return installPanelSetup(arguments[0], arguments[1:], os.Stdin, output)
	case "enrollment-runner", "enrollment-recover":
		operation, err := runnerOperation(arguments)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return hostsetup.RunEnrollment(ctx, operation, arguments[0] == "enrollment-recover")
	case "final-runner", "final-recover":
		operation, err := runnerOperation(arguments)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return hostsetup.RunFinal(ctx, operation, arguments[0] == "final-recover")
	}
	return errors.New("usage: panel install [--source PATH --source-sha SHA --operator NAME --operator-key KEY] [--yes]")
}

func runnerOperation(arguments []string) (string, error) {
	if len(arguments) != 3 || arguments[1] != "--operation-id" {
		return "", errors.New("invalid installer runner arguments")
	}
	return arguments[2], nil
}
