package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// Loopback is the development HTTP path. Production never falls back here.
func Loopback(ctx context.Context, address string) ([]net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("development listen address must use a loopback IP")
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	return []net.Listener{listener}, nil
}

// Serve owns the complete set of already bound listeners. A failed accept/serve
// closes all siblings; cancellation drains HTTP within the existing 10-second budget.
func Serve(ctx context.Context, server *http.Server, listeners []net.Listener) error {
	if server == nil || len(listeners) < 1 || len(listeners) > 2 {
		return errors.New("invalid HTTP listener set")
	}
	done := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func() {
			var err error
			if server.TLSConfig == nil {
				err = server.Serve(listener)
			} else {
				err = server.ServeTLS(listener, "", "")
			}
			done <- err
		}()
	}
	var result error
	finished := 0
	select {
	case result = <-done:
		finished = 1
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if result != nil && !errors.Is(result, http.ErrServerClosed) {
		_ = server.Close()
	} else {
		result = server.Shutdown(shutdown)
		if result != nil {
			_ = server.Close()
		}
	}
	for finished < len(listeners) {
		select {
		case err := <-done:
			finished++
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				result = errors.Join(result, err)
			}
		case <-shutdown.Done():
			_ = server.Close()
			return errors.Join(result, shutdown.Err())
		}
	}
	if errors.Is(result, http.ErrServerClosed) {
		return nil
	}
	return result
}
