package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/installation"
)

func installPanelSetup(branch string, arguments []string, input *os.File, output io.Writer) error {
	flags := flag.NewFlagSet("panel install "+branch, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var operationID string
	if branch == "panel-proof" {
		flags.StringVar(&operationID, "operation-id", "", "retained installation operation UUID")
	}
	if flags.Parse(arguments) != nil || flags.NArg() != 0 {
		return errors.New("invalid panel setup arguments")
	}
	if installation.CheckRuntimeUser() != nil {
		return errors.New("panel setup must run as the dedicated panel identity")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var result installation.Result
	var err error
	if branch == "panel-bootstrap" {
		handoff, readErr := installation.ReadHandoff(ctx, input)
		if readErr != nil {
			return readErr
		}
		result, err = installation.Bootstrap(ctx, handoff)
	} else {
		result, err = installation.Prove(ctx, operationID)
	}
	if err != nil {
		return err
	}
	if json.NewEncoder(output).Encode(result) != nil {
		return errors.New("panel setup response unavailable; inspect retained installation state before retrying")
	}
	return nil
}
