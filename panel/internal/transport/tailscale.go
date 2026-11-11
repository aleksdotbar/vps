package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"time"

	"tailscale.com/client/local"
)

const handshakeTimeout = 5 * time.Second

// Tailnet uses one native read/certificate client, never an embedded Tailscale
// node or operator-capable proxy. Binding failure closes every opened listener.
func Tailnet(ctx context.Context, origin string, client *local.Client) (*tls.Config, []net.Listener, error) {
	return tailnet(ctx, origin, client, (&net.ListenConfig{}).Listen)
}
func tailnet(ctx context.Context, origin string, client *local.Client, listen func(context.Context, string, string) (net.Listener, error)) (*tls.Config, []net.Listener, error) {
	if client == nil || !client.UseSocketOnly {
		return nil, nil, ErrIdentity
	}
	statusCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	status, err := client.StatusWithoutPeers(statusCtx)
	cancel()
	if err != nil {
		return nil, nil, ErrIdentity
	}
	selected, err := identityFor(origin, status)
	if err != nil {
		return nil, nil, err
	}
	// Prove certificate access before opening sockets. The daemon owns cache,
	// renewal and ACME; private key bytes stay out of files/argv/logs here.
	certificateCtx, cancel := context.WithTimeout(ctx, time.Minute)
	_, err = certificate(certificateCtx, client, selected.hostname)
	cancel()
	if err != nil {
		return nil, nil, err
	}
	configuration := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Full handshakes ensure every new connection revalidates native identity.
		SessionTicketsDisabled: true,
		GetCertificate:         certificateSelector(origin, selected, client), //nolint:contextcheck // Native TLS callbacks inherit ClientHello.Context, independently of startup; cancellation regression verifies this boundary.
	}
	listeners := make([]net.Listener, 0, len(selected.addresses))
	for _, address := range selected.addresses {
		network := "tcp6"
		if address.Is4() {
			network = "tcp4"
		}
		listener, err := listen(ctx, network, net.JoinHostPort(address.String(), "443"))
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, nil, err
		}
		listeners = append(listeners, listener)
	}
	return configuration, listeners, nil
}
func certificate(ctx context.Context, client *local.Client, hostname string) (*tls.Certificate, error) {
	certificatePEM, keyPEM, err := client.CertPair(ctx, hostname)
	defer clear(keyPEM)
	if err != nil {
		return nil, ErrCertificate
	}
	pair, err := tls.X509KeyPair(certificatePEM, keyPEM)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, ErrCertificate
	}
	leaf := pair.Leaf
	if leaf == nil {
		leaf, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, ErrCertificate
		}
	}
	now := time.Now()
	if leaf.VerifyHostname(hostname) != nil || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, ErrCertificate
	}
	return &pair, nil
}

// Handshake callbacks inherit ClientHello.Context independently of startup.
func certificateSelector(origin string, selected identity, client *local.Client) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	permits := make(chan struct{}, 4)
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if hello == nil || hello.ServerName != selected.hostname {
			return nil, ErrIdentity
		}
		select {
		case permits <- struct{}{}:
			defer func() { <-permits }()
		default:
			return nil, ErrBusy
		}
		bounded, cancel := context.WithTimeout(hello.Context(), handshakeTimeout)
		defer cancel()
		current, err := client.StatusWithoutPeers(bounded)
		if err != nil {
			return nil, ErrIdentity
		}
		verified, err := identityFor(origin, current)
		if err != nil || !selected.same(verified) {
			return nil, ErrIdentity
		}
		return certificate(bounded, client, selected.hostname)
	}
}
