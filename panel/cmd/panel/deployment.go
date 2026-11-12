package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os/signal"
	"syscall"

	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
)

// These root-only commands are invoked by admitted native transient units. They
// accept identities only; commands, paths and secret values are not arguments.
func deploymentCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	operation := flags.String("operation-id", "", "retained operation identity")
	var environment string
	var recovery bool
	if command == "deploy-runner" {
		flags.StringVar(&environment, "environment-id", "", "retained environment identity")
		flags.BoolVar(&recovery, "recover", false, "recover retained previous release")
	}
	if flags.Parse(args) != nil || flags.NArg() != 0 || *operation == "" || command == "deploy-runner" && environment == "" {
		return errors.New("invalid native deployment command")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if command == "deploy-registry-cleanup" {
		return hosthelper.RunDeploymentRegistryCleanup(ctx, *operation)
	}
	if command != "deploy-runner" {
		return errors.New("unknown native deployment command")
	}
	return hosthelper.RunDeployment(ctx, environment, *operation, recovery, hosthelper.AcquireHost)
}
