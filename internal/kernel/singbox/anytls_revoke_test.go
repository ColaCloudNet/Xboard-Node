package singbox

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing/common/logger"
	singM "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// TestAnyTLSUpdateUsersRevokesReusedSession is the panel-expiry repro:
// sing-box anytls calls Service.UpdateUsers when a user disappears. The
// password is checked only at handshake, so an already-open multiplexed
// session must be closed and a fresh handshake with that password must fail.
func TestAnyTLSUpdateUsersRevokesReusedSession(t *testing.T) {
	handler := &anytlsHandler{connections: make(chan net.Conn, 4)}
	service, err := anytls.NewService(anytls.ServiceConfig{
		PaddingScheme: padding.DefaultPaddingScheme,
		Users: []anytls.User{
			{Name: "uuid-alice", Password: "uuid-alice"},
			{Name: "uuid-bob", Password: "uuid-bob"},
		},
		Handler: handler,
		Logger:  logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}

	alice, aliceStream := dialAnyTLS(t, service, "uuid-alice")
	defer alice.Close()
	bob, bobStream := dialAnyTLS(t, service, "uuid-bob")
	defer bob.Close()
	waitAnyTLS(t, handler)
	waitAnyTLS(t, handler)

	bobRead := readAnyTLS(bobStream)
	// Panel stops sending bob. Name and password are both the user UUID.
	service.UpdateUsers([]anytls.User{{Name: "uuid-alice", Password: "uuid-alice"}})

	select {
	case readErr := <-bobRead:
		if readErr == nil {
			t.Fatal("reused anytls session kept reading after the user was removed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reused anytls session stayed alive after UpdateUsers removed the user")
	}

	if err := expectAnyTLSTimeout(aliceStream, 200*time.Millisecond); err != nil {
		t.Fatalf("user who is still authorized was disconnected: %v", err)
	}
	if _, err := bobStream.Write([]byte("x")); err == nil {
		t.Fatal("removed session accepted a write after UpdateUsers")
	}
	assertAnyTLSPasswordRejected(t, service, "uuid-bob")

	again, err := alice.CreateProxy(context.Background(), singM.ParseSocksaddr("example.org:443"))
	if err != nil {
		t.Fatalf("authorized user could not open a new stream: %v", err)
	}
	defer again.Close()
	waitAnyTLS(t, handler)
}

func dialAnyTLS(t *testing.T, service *anytls.Service, password string) (*anytls.Client, net.Conn) {
	t.Helper()
	client, err := anytls.NewClient(context.Background(), anytls.ClientConfig{
		Password: password,
		DialOut:  anytlsDial(service),
		Logger:   logger.NOP(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.CreateProxy(context.Background(), singM.ParseSocksaddr("example.org:80"))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, stream
}

func anytlsDial(service *anytls.Service) func(context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		clientConn, serverConn := net.Pipe()
		go func() {
			err := service.NewConnection(ctx, serverConn, singM.ParseSocksaddr("127.0.0.1:443"), nil)
			if err != nil {
				serverConn.Close()
			}
		}()
		return clientConn, nil
	}
}

func waitAnyTLS(t *testing.T, handler *anytlsHandler) {
	t.Helper()
	select {
	case <-handler.connections:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for the relayed anytls stream")
	}
}

func readAnyTLS(conn net.Conn) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		done <- err
	}()
	return done
}

func expectAnyTLSTimeout(conn net.Conn, d time.Duration) error {
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

func assertAnyTLSPasswordRejected(t *testing.T, service *anytls.Service, password string) {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	errCh := make(chan error, 1)
	go func() {
		errCh <- service.NewConnection(context.Background(), serverConn, singM.ParseSocksaddr("127.0.0.1:443"), nil)
	}()
	sum := sha256.Sum256([]byte(password))
	pkt := append(sum[:], 0, 0)
	if _, err := clientConn.Write(pkt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("fresh auth succeeded for removed password %q", password)
		}
		if !strings.Contains(err.Error(), "unknown user password") {
			t.Fatalf("fresh auth error = %v, want unknown user password", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fresh anytls auth to fail")
	}
}

type anytlsHandler struct {
	connections chan net.Conn
}

func (h *anytlsHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source singM.Socksaddr, destination singM.Socksaddr, onClose N.CloseHandlerFunc) {
	h.connections <- conn
}
