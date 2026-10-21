package anytls

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// TestUpdateUsersClosesRemovedUserSessions verifies that removing a password
// also closes the multiplexed session that password already opened. anytls
// authenticates once, by the hash in the first 32 bytes, and then carries any
// number of streams without looking at that password again.
func TestUpdateUsersClosesRemovedUserSessions(t *testing.T) {
	handler := &testHandler{connections: make(chan net.Conn, 4)}
	service, err := NewService(ServiceConfig{
		PaddingScheme: padding.DefaultPaddingScheme,
		Users: []User{
			{Name: "alice", Password: "alice-secret"},
			{Name: "bob", Password: "bob-secret"},
		},
		Handler: handler,
		Logger:  logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}

	aliceClient, aliceProxy := dialUser(t, service, "alice-secret")
	defer aliceClient.Close()
	bobClient, bobProxy := dialUser(t, service, "bob-secret")
	defer bobClient.Close()

	waitRelayed(t, handler)
	waitRelayed(t, handler)

	bobRead := readAsync(bobProxy)
	service.UpdateUsers([]User{{Name: "alice", Password: "alice-secret"}})

	select {
	case readErr := <-bobRead:
		if readErr == nil {
			t.Fatal("removed user's open stream kept reading")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session of a removed user stayed alive")
	}

	// The kept user's session must stay up. A short read deadline distinguishes
	// "still open" (timeout) from "kicked" (immediate close).
	if err := expectReadTimeout(aliceProxy, 200*time.Millisecond); err != nil {
		t.Fatalf("kept user session was closed: %v", err)
	}

	// The closed session must not accept more data, and a fresh handshake with
	// the removed password must fail. The kept user can still open a stream.
	if _, err := bobProxy.Write([]byte("x")); err == nil {
		t.Fatal("removed session accepted a write after UpdateUsers")
	}
	assertPasswordRejected(t, service, "bob-secret")

	aliceAgain, err := aliceClient.CreateProxy(context.Background(), M.ParseSocksaddr("example.org:443"))
	if err != nil {
		t.Fatalf("kept user fresh stream: %v", err)
	}
	defer aliceAgain.Close()
	waitRelayed(t, handler)
}

// TestUpdateUsersPasswordRotationClosesOldSession ensures a password change
// (same name, new secret) revokes the session that authenticated with the old
// secret. Panel UUIDs are used as both name and password, so a UUID rotation
// hits this path.
func TestUpdateUsersPasswordRotationClosesOldSession(t *testing.T) {
	handler := &testHandler{connections: make(chan net.Conn, 2)}
	service, err := NewService(ServiceConfig{
		PaddingScheme: padding.DefaultPaddingScheme,
		Users:         []User{{Name: "alice", Password: "old-secret"}},
		Handler:       handler,
		Logger:        logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}

	client, proxy := dialUser(t, service, "old-secret")
	defer client.Close()
	waitRelayed(t, handler)

	readDone := readAsync(proxy)
	service.UpdateUsers([]User{{Name: "alice", Password: "new-secret"}})
	select {
	case readErr := <-readDone:
		if readErr == nil {
			t.Fatal("session authenticated with the old password stayed alive")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session of a rotated password stayed alive")
	}

	if _, err := proxy.Write([]byte("x")); err == nil {
		t.Fatal("rotated password's session accepted a write")
	}
	assertPasswordRejected(t, service, "old-secret")
}

// assertPasswordRejected performs a fresh handshake and requires the service
// to refuse the password. This is the path a new client takes after expiry.
func assertPasswordRejected(t *testing.T, service *Service, password string) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.NewConnection(context.Background(), serverConn, M.ParseSocksaddr("127.0.0.1:1080"), nil)
	}()
	sum := sha256.Sum256([]byte(password))
	pkt := append(sum[:], 0, 0) // 32-byte hash, padding length 0
	if _, err := clientConn.Write(pkt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("fresh auth succeeded for %q", password)
		}
		if !strings.Contains(err.Error(), "unknown user password") {
			t.Fatalf("fresh auth error = %v, want unknown user password", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fresh auth to fail")
	}
}

func dialUser(t *testing.T, service *Service, password string) (*Client, net.Conn) {
	t.Helper()
	client, err := NewClient(context.Background(), ClientConfig{
		Password: password,
		DialOut:  serviceDial(service),
		Logger:   logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := client.CreateProxy(context.Background(), M.ParseSocksaddr("example.org:80"))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, proxy
}

// serviceDial hands each outbound connection to Service.NewConnection. A
// rejected handshake closes the pipe so the client observes the failure
// instead of blocking in the session read loop.
func serviceDial(service *Service) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			err := service.NewConnection(ctx, serverConn, M.ParseSocksaddr("127.0.0.1:1080"), nil)
			if err != nil {
				serverConn.Close()
			}
		}()
		return clientConn, nil
	}
}

func waitRelayed(t *testing.T, handler *testHandler) {
	t.Helper()
	select {
	case <-handler.connections:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the relayed connection")
	}
}

func readAsync(conn net.Conn) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		done <- err
	}()
	return done
}

func expectReadTimeout(conn net.Conn, d time.Duration) error {
	_ = conn.SetReadDeadline(time.Now().Add(d))
	_, err := conn.Read(make([]byte, 1))
	if err == nil {
		return errors.New("read returned data")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return nil
	}
	return err
}

type testHandler struct {
	connections chan net.Conn
}

func (h *testHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	h.connections <- conn
}
