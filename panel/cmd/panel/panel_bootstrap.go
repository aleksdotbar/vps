package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aleksdotbar/vps/panel/internal/installation"
)

// setupAsPanel runs the registration and private-proof steps as the panel user,
// which owns the database and master key; `panel setup` launches it via runuser.
func setupAsPanel(step string, input *os.File, output io.Writer) error {
	if installation.CheckRuntimeUser() != nil {
		return errors.New("this setup step must run as the dedicated panel identity")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var result installation.Result
	var err error
	if step == "bootstrap" {
		handoff, readErr := installation.ReadHandoff(ctx, input)
		if readErr != nil {
			return readErr
		}
		result, err = installation.Bootstrap(ctx, handoff)
	} else {
		result, err = installation.Prove(ctx)
	}
	if err != nil {
		return err
	}
	if json.NewEncoder(output).Encode(result) != nil {
		return errors.New("setup step response unavailable; rerun sudo panel setup")
	}
	return nil
}
