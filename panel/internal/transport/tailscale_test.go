package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

type localFixture struct {
	mu                sync.Mutex
	status            *ipnstate.Status
	pair              []byte
	certificates      atomic.Int64
	blockCertificates atomic.Bool
	started           chan struct{}
	blocked           atomic.Bool
}

func localClient(t *testing.T) (*local.Client, *localFixture, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: fixtureName}, DNSNames: []string{fixtureName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certificate) {
		t.Fatal("fixture CA unavailable")
	}
	fixture := &localFixture{status: nativeStatus(), pair: append(private, certificate...), started: make(chan struct{}, 1)}
	return &local.Client{UseSocketOnly: true, Transport: fixture}, fixture, pool
}
func (f *localFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	switch request.URL.Path {
	case "/localapi/v0/status":
		if request.URL.Query().Get("peers") != "false" {
			return nil, errors.New("fixture requires peerless status")
		}
		f.mu.Lock()
		value, err := json.Marshal(f.status)
		f.mu.Unlock()
		if err != nil {
			return nil, err
		}
		body = value
	case "/localapi/v0/cert/" + fixtureName:
		f.certificates.Add(1)
		if f.blockCertificates.Load() {
			f.blocked.Store(true)
			select {
			case f.started <- struct{}{}:
			default:
			}
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		body = bytes.Clone(f.pair)
	default:
		return nil, errors.New("unexpected native LocalAPI request")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}
func testBindings(t *testing.T) (func(context.Context, string, string) (net.Listener, error), *[]net.Listener, *[]string) {
	t.Helper()
	listeners := make([]net.Listener, 0, 2)
	addresses := make([]string, 0, 2)
	listen := func(ctx context.Context, network, address string) (net.Listener, error) {
		addresses = append(addresses, network+" "+address)
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
		if err == nil {
			listeners = append(listeners, listener)
		}
		return listener, err
	}
	return listen, &listeners, &addresses
}
func TestPrivateTLSBindsBothSelfAddressesAndShutsDownTogether(t *testing.T) {
	client, fixture, roots := localClient(t)
	listen, listeners, addresses := testBindings(t)
	configuration, bound, err := tailnet(context.Background(), "https://"+fixtureName, client, listen)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 2 || len(*addresses) != 2 || (*addresses)[0] != "tcp4 100.80.1.2:443" || (*addresses)[1] != "tcp6 [fd7a:115c:a1e0::12]:443" {
		t.Fatalf("private bindings changed: %v", *addresses)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), TLSConfig: configuration, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, server, bound) }()
	sessionCache := tls.NewLRUClientSessionCache(4)
	for _, listener := range *listeners {
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, ClientSessionCache: sessionCache}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		}}
		httpClient := &http.Client{Transport: transport, Timeout: time.Second}
		request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://"+fixtureName+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(response.Body); err != nil {
			_ = response.Body.Close()
			t.Fatal(err)
		}
		_ = response.Body.Close()
		transport.CloseIdleConnections()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal("private request not served")
		}
	}
	// A changed native identity must deny a new connection even with a valid
	// cached certificate for the previous name. Existing sessions need no watcher.
	fixture.mu.Lock()
	fixture.status.Self.ID = "nReplaced"
	fixture.mu.Unlock()
	connection, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: time.Second}, Config: &tls.Config{RootCAs: roots, ServerName: fixtureName, MinVersion: tls.VersionTLS12, ClientSessionCache: sessionCache}}).DialContext(t.Context(), "tcp", bound[len(bound)-1].Addr().String())
	if err == nil {
		_ = connection.Close()
		t.Fatal("changed native identity accepted")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("private listeners did not shut down")
	}
	for _, listener := range bound {
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(t.Context(), "tcp", listener.Addr().String())
		if err == nil {
			_ = connection.Close()
			t.Fatal("listener remained open after shutdown")
		}
	}
}
func TestWrongSNIFailsBeforeCertificateAndPartialBindRollsBack(t *testing.T) {
	client, fixture, _ := localClient(t)
	listen, opened, _ := testBindings(t)
	calls := 0
	failSecond := func(ctx context.Context, network, address string) (net.Listener, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("fixture bind failure")
		}
		return listen(ctx, network, address)
	}
	if _, _, err := tailnet(context.Background(), "https://"+fixtureName, client, failSecond); err == nil {
		t.Fatal("partial bind failure ignored")
	}
	if len(*opened) != 1 {
		t.Fatal("fixture did not open first listener")
	}
	connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(t.Context(), "tcp", (*opened)[0].Addr().String())
	if err == nil {
		_ = connection.Close()
		t.Fatal("partial listener leaked")
	}
	before := fixture.certificates.Load()
	listen, _, _ = testBindings(t)
	configuration, bound, err := tailnet(context.Background(), "https://"+fixtureName, client, listen)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, listener := range bound {
			_ = listener.Close()
		}
	}()
	if fixture.certificates.Load() != before+1 {
		t.Fatal("startup did not prove certificate permission")
	}
	if _, err := configuration.GetCertificate(&tls.ClientHelloInfo{ServerName: "alias.generic.ts.net"}); !errors.Is(err, ErrIdentity) {
		t.Fatalf("SNI alias accepted: %v", err)
	}
	if fixture.certificates.Load() != before+1 {
		t.Fatal("rejected SNI fetched another certificate")
	}
}
func TestClientHelloCancellationReachesNativeCertificateRequest(t *testing.T) {
	client, fixture, roots := localClient(t)
	listen, _, _ := testBindings(t)
	configuration, bound, err := tailnet(context.Background(), "https://"+fixtureName, client, listen)
	if err != nil {
		t.Fatal(err)
	}
	for _, listener := range bound {
		_ = listener.Close()
	}
	fixture.blockCertificates.Store(true)
	serverSide, clientSide := net.Pipe()
	defer func() { _ = serverSide.Close() }()
	defer func() { _ = clientSide.Close() }()
	server := tls.Server(serverSide, configuration)
	browser := tls.Client(clientSide, &tls.Config{RootCAs: roots, ServerName: fixtureName, MinVersion: tls.VersionTLS12})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	browserDone := make(chan error, 1)
	go func() { serverDone <- server.HandshakeContext(ctx) }()
	go func() { browserDone <- browser.HandshakeContext(ctx) }()
	select {
	case <-fixture.started:
	case <-time.After(time.Second):
		t.Fatal("certificate request did not start")
	}
	cancel()
	select {
	case err := <-serverDone:
		if err == nil {
			t.Fatal("canceled server handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("ClientHello cancellation did not reach native certificate request")
	}
	select {
	case err := <-browserDone:
		if err == nil {
			t.Fatal("canceled client handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client handshake did not cancel")
	}
	if !fixture.blocked.Load() {
		t.Fatal("fixture did not block native certificate IO")
	}
}
func TestUnreadyPrivateIdentityDoesNotBindOrFallBack(t *testing.T) {
	client, fixture, _ := localClient(t)
	fixture.status.TUN = false
	calls := 0
	_, _, err := tailnet(context.Background(), "https://"+fixtureName, client, func(context.Context, string, string) (net.Listener, error) {
		calls++
		return nil, errors.New("unexpected bind")
	})
	if !errors.Is(err, ErrIdentity) || calls != 0 || fixture.certificates.Load() != 0 {
		t.Fatal("unready identity attempted bind/certificate fallback")
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "203.0.113.1:8080", "[::]:8080"} {
		if _, err := Loopback(context.Background(), address); err == nil {
			t.Fatalf("public development binding accepted: %s", address)
		}
	}
}
