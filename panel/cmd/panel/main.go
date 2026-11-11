package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/aleksdotbar/vps/panel/internal/compose"
	"github.com/riverqueue/river"
	"golang.org/x/term"
	"tailscale.com/client/local"

	"github.com/aleksdotbar/vps/panel/internal/api"
	"github.com/aleksdotbar/vps/panel/internal/auth"
	"github.com/aleksdotbar/vps/panel/internal/cloudflareflow"
	"github.com/aleksdotbar/vps/panel/internal/executor"
	"github.com/aleksdotbar/vps/panel/internal/githubflow"
	"github.com/aleksdotbar/vps/panel/internal/hosthelper"
	"github.com/aleksdotbar/vps/panel/internal/operations"
	"github.com/aleksdotbar/vps/panel/internal/privatefile"
	"github.com/aleksdotbar/vps/panel/internal/projects"
	"github.com/aleksdotbar/vps/panel/internal/readiness"
	"github.com/aleksdotbar/vps/panel/internal/remote"
	"github.com/aleksdotbar/vps/panel/internal/securestate"
	"github.com/aleksdotbar/vps/panel/internal/store"
	"github.com/aleksdotbar/vps/panel/internal/transport"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("panel stopped", "error", err)
		os.Exit(1)
	}
}

func run() (returnErr error) {
	command := "serve"
	arguments := os.Args[1:]
	if len(arguments) > 0 && arguments[0] == "host-helper" {
		if len(arguments) != 1 {
			return errors.New("host-helper accepts no arguments")
		}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		return hosthelper.Serve(ctx, os.Stdin, os.Stdout, finalAuthority)
	}
	if len(arguments) > 0 && (arguments[0] == "deploy-runner" || arguments[0] == "deploy-registry-cleanup") {
		return deploymentCommand(arguments[0], arguments[1:])
	}
	if len(arguments) > 0 && arguments[0] == "install" {
		return install(arguments[1:], os.Stdout)
	}
	if len(arguments) > 0 && (arguments[0] == "bootstrap" || arguments[0] == "reset-password" || arguments[0] == "serve") {
		command = arguments[0]
		arguments = arguments[1:]
	}
	flags := flag.NewFlagSet("panel "+command, flag.ContinueOnError)
	showVersion := flags.Bool("version", false, "print version and exit")
	tailscaleHTTPS := flags.Bool("tailscale-https", false, "serve HTTPS only on verified self Tailscale addresses")
	listen := flags.String("listen", "127.0.0.1:8080", "loopback HTTP address behind private HTTPS")
	directory := flags.String("state-dir", "", "absolute private state directory")
	keyPath := flags.String("master-key-file", "", "absolute protected master key file outside SQLite")
	composeCommand := flags.String("compose-command", "", "absolute pinned standalone Docker Compose executable")
	origin := flags.String("origin", "", "configured private HTTPS origin")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *directory == "" || *keyPath == "" {
		return errors.New("state-dir and master-key-file are required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	lock, err := privatefile.LockProcess(*directory)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	if command == "bootstrap" {
		password, err := readPassword()
		if err != nil {
			return err
		}
		defer clear(password)
		return auth.Bootstrap(ctx, *directory, *keyPath, password)
	}
	key, err := privatefile.ReadKey(*keyPath)
	if err != nil {
		return err
	}
	defer clear(key)
	cipher, err := securestate.NewCipher(key)
	if err != nil {
		return err
	}

	database, err := store.Open(ctx, *directory, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
	authentication, err := auth.New(ctx, database, cipher)
	if err != nil {
		return err
	}
	if command == "reset-password" {
		password, err := readPassword()
		if err != nil {
			return err
		}
		defer clear(password)
		return authentication.Reset(ctx, password)
	}
	var tlsConfiguration *tls.Config
	var listeners []net.Listener
	if *tailscaleHTTPS {
		explicitListen := false
		flags.Visit(func(value *flag.Flag) {
			if value.Name == "listen" {
				explicitListen = true
			}
		})
		if explicitListen {
			return errors.New("listen cannot be set with tailscale-https")
		}
		tlsConfiguration, listeners, err = transport.Tailnet(ctx, *origin, &local.Client{UseSocketOnly: true})
	} else {
		listeners, err = transport.Loopback(ctx, *listen)
	}
	if err != nil {
		return err
	}
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	resolver, err := compose.New(*composeCommand)
	if err != nil {
		return err
	}
	metadata := projects.New(database, cipher)
	integration := githubflow.New(database, cipher, *origin, http.DefaultTransport)
	cloudflare := cloudflareflow.New(database, cipher, http.DefaultTransport)
	helperClient, helperError := remote.New("/run/current-system/sw/bin/ssh", "/run/panel/ssh")
	if helperError != nil {
		if *tailscaleHTTPS {
			return errors.New("initialize verified host SSH transport")
		}
		helperClient = nil
		slog.Info("native SSH transport unavailable in loopback development; native server operations disabled")
	}
	var tunnels *cloudflareflow.Tunnels
	var builds *githubflow.Builds
	deploymentExecutor := executor.New(database, cipher, helperClient, cloudflare)
	queue, err := operations.New(ctx, database, deploymentExecutor, func(workers *river.Workers, runtime *operations.Runtime) ([]*river.PeriodicJob, error) {
		builds = githubflow.NewBuilds(integration, metadata, resolver, runtime)
		tunnels = cloudflareflow.NewTunnels(cloudflare, helperClient, runtime)
		if err := tunnels.Register(workers); err != nil {
			return nil, err
		}
		buildJobs, err := builds.Register(workers)
		if err != nil {
			return nil, err
		}
		hostJobs, err := readiness.New(database, cipher, helperClient).Register(workers)
		if err != nil {
			return nil, err
		}
		return append(buildJobs, hostJobs...), nil
	})
	if err != nil {
		return err
	}
	handler, cleanup, err := api.New(database, authentication, *origin, metadata, integration, builds, cloudflare, tunnels)
	if err != nil {
		return err
	}
	defer cleanup()

	// Signals enter the bounded shutdown path rather than cancelling remote work implicitly.
	if err := queue.Start(context.WithoutCancel(ctx)); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, queue.Stop(stopCtx))
	}()
	server := &http.Server{Handler: handler, TLSConfig: tlsConfiguration, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	return transport.Serve(ctx, server, listeners)
}

func readPassword() ([]byte, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		if _, err := fmt.Fprint(os.Stderr, "Administrator password: "); err != nil {
			return nil, err
		}
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		if err != nil {
			return nil, errors.New("read protected password input")
		}
		if _, err := fmt.Fprint(os.Stderr, "\nConfirm password: "); err != nil {
			clear(first)
			return nil, err
		}
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		defer clear(second)
		if _, writeErr := fmt.Fprintln(os.Stderr); writeErr != nil {
			clear(first)
			return nil, writeErr
		}
		if err != nil || !bytes.Equal(first, second) {
			clear(first)
			return nil, errors.New("password confirmation failed")
		}
		return first, nil
	}
	// Protected pipes support installation automation; never accept passwords in
	// arguments, environment variables or a pathname supplied by an HTTP client.
	password, err := io.ReadAll(io.LimitReader(os.Stdin, 1027))
	if err != nil {
		return nil, errors.New("read protected password input")
	}
	if len(password) > 1026 {
		clear(password)
		return nil, auth.ErrPassword
	}
	password = bytes.TrimSuffix(password, []byte("\n"))
	password = bytes.TrimSuffix(password, []byte("\r"))
	if !utf8.Valid(password) || len(password) > 1024 || bytes.ContainsAny(password, "\r\n") {
		clear(password)
		return nil, auth.ErrPassword
	}
	return password, nil
}
