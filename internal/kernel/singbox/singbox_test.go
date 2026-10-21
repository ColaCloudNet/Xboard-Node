package singbox

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/ColaCloudNet/Xboard-Node/internal/config"
	"github.com/ColaCloudNet/Xboard-Node/internal/kernel"
	"github.com/ColaCloudNet/Xboard-Node/internal/model"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	singM "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"golang.org/x/time/rate"
)

func TestSingBoxCapabilities(t *testing.T) {
	s := New(config.KernelConfig{Type: "sing-box"})
	caps := s.Capabilities()
	if !caps.PerUserSpeedLimit || !caps.DeviceLimit || !caps.AliveIPTracking || !caps.ForceCloseUser {
		t.Fatalf("unexpected sing-box capabilities: %+v", caps)
	}
	if caps.BuiltInTrafficStats || caps.ForceCloseConnection {
		t.Fatalf("unexpected sing-box capabilities: %+v", caps)
	}
	protocols := s.Protocols()
	found := false
	for _, protocol := range protocols {
		if protocol == "hysteria" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Protocols() missing hysteria: %v", protocols)
	}
}

type testConn struct {
	closed bool
	reads  [][]byte
	writes [][]byte
}

func (c *testConn) Read(b []byte) (int, error) {
	if len(c.reads) == 0 {
		return 0, errors.New("eof")
	}
	chunk := c.reads[0]
	c.reads = c.reads[1:]
	n := copy(b, chunk)
	return n, nil
}

func (c *testConn) Write(b []byte) (int, error) {
	cp := append([]byte(nil), b...)
	c.writes = append(c.writes, cp)
	return len(b), nil
}

func (c *testConn) Close() error                     { c.closed = true; return nil }
func (c *testConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *testConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *testConn) SetDeadline(time.Time) error      { return nil }
func (c *testConn) SetReadDeadline(time.Time) error  { return nil }
func (c *testConn) SetWriteDeadline(time.Time) error { return nil }

func testInboundContext(uuid, ip string) adapter.InboundContext {
	return adapter.InboundContext{
		User:   uuid,
		Source: singM.Socksaddr{Addr: netip.MustParseAddr(ip)},
	}
}

func TestConnTrackerRoutedConnectionTracksTrafficAndAliveIPs(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	base := &testConn{reads: [][]byte{[]byte("hello")}}

	wrapped := tracker.RoutedConnection(context.Background(), base, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)

	buf := make([]byte, 16)
	n, err := wrapped.Read(buf)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if n != 5 {
		t.Fatalf("Read() bytes = %d, want 5", n)
	}
	if _, err := wrapped.Write([]byte("bye")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	traffic, aliveIPs, connCount := tracker.GetUserTraffic()
	if got := traffic[1]; got != [2]int64{5, 3} {
		t.Fatalf("traffic[1] = %v, want [5 3]", got)
	}
	if !aliveIPs[1]["1.1.1.1"] {
		t.Fatal("expected alive IP to include source address")
	}
	if connCount != 1 {
		t.Fatalf("connCount = %d, want 1", connCount)
	}

	if err := wrapped.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	_, aliveIPs, connCount = tracker.GetUserTraffic()
	if aliveIPs[1] != nil {
		t.Fatalf("aliveIPs after close = %v, want nil", aliveIPs[1])
	}
	if connCount != 0 {
		t.Fatalf("connCount after close = %d, want 0", connCount)
	}
}

func TestConnTrackerRoutedConnectionRejectsWhenDeviceLimitExceeded(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetDeviceLimitFunc(func(uuid string) (int, bool) {
		if uuid != "uuid-1" {
			return 0, false
		}
		return 1, true
	})

	first := &testConn{}
	wrapped1 := tracker.RoutedConnection(context.Background(), first, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)
	if wrapped1 == first {
		t.Fatal("expected first connection to be wrapped")
	}

	second := &testConn{}
	wrapped2 := tracker.RoutedConnection(context.Background(), second, testInboundContext("uuid-1", "2.2.2.2"), nil, nil)
	if wrapped2 != second {
		t.Fatal("expected rejected connection to be returned unwrapped")
	}
	if !second.closed {
		t.Fatal("expected rejected connection to be closed")
	}
}

func TestConnTrackerCheckDeviceGateMergesFreshGlobalDevices(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	us := tracker.users[1]
	us.addConn("1.1.1.1")
	tracker.UpdateGlobalDevices(map[int][]string{1: {"9.9.9.9"}})

	if tracker.checkDeviceGate(us, 1, "2.2.2.2", 2) {
		t.Fatal("expected lexicographically earlier candidate to remain allowed")
	}
	if !tracker.checkDeviceGate(us, 1, "99.99.99.99", 2) {
		t.Fatal("expected merged local+global device state to reject lexicographically later third device")
	}
	if tracker.checkDeviceGate(us, 1, "1.1.1.1", 2) {
		t.Fatal("existing local IP should still be allowed")
	}
	if tracker.checkDeviceGate(us, 1, "9.9.9.9", 2) {
		t.Fatal("existing global IP should still be allowed")
	}
}

func TestConnTrackerCloseByUUIDClosesOnlyThatUser(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-gone": 1, "uuid-stay": 2})

	goneA := &testConn{}
	goneB := &testConn{}
	stay := &testConn{}
	wrappedA := tracker.RoutedConnection(context.Background(), goneA, testInboundContext("uuid-gone", "1.1.1.1"), nil, nil)
	wrappedB := tracker.RoutedConnection(context.Background(), goneB, testInboundContext("uuid-gone", "2.2.2.2"), nil, nil)
	tracker.RoutedConnection(context.Background(), stay, testInboundContext("uuid-stay", "3.3.3.3"), nil, nil)

	if got := tracker.CloseByUUID("uuid-gone"); got != 2 {
		t.Fatalf("CloseByUUID() = %d, want 2", got)
	}
	if !goneA.closed || !goneB.closed {
		t.Fatal("expected both connections for the removed UUID to close")
	}
	if stay.closed {
		t.Fatal("expected the remaining user's connection to stay open")
	}
	if got := tracker.ActiveCount(); got != 1 {
		t.Fatalf("ActiveCount() = %d, want 1", got)
	}
	// The closed wrappers must leave the index so a second kick is a no-op
	// and CloseByID cannot see them.
	if got := tracker.CloseByUUID("uuid-gone"); got != 0 {
		t.Fatalf("second CloseByUUID() = %d, want 0", got)
	}
	trackedA := wrappedA.(*trackedConn)
	trackedB := wrappedB.(*trackedConn)
	if tracker.CloseByID(trackedA.connID) || tracker.CloseByID(trackedB.connID) {
		t.Fatal("CloseByID still found a connection CloseByUUID already dropped")
	}
	if tracker.CloseByUUID("") != 0 {
		t.Fatal("CloseByUUID(empty) should be a no-op")
	}
	if stay.closed {
		t.Fatal("remaining connection closed by the empty-UUID call")
	}
}

func TestSingBoxCloseUUIDsLockedUsesConnTracker(t *testing.T) {
	s := New(config.KernelConfig{Type: "sing-box"})
	s.connTracker = NewConnTracker(0)
	s.users = []model.UserSpec{
		{ID: 1, UUID: "uuid-old"},
		{ID: 2, UUID: "uuid-stay"},
	}
	s.connTracker.SetUserMap(buildUserMap(s.users))

	oldConn := &testConn{}
	stayConn := &testConn{}
	s.connTracker.RoutedConnection(context.Background(), oldConn, testInboundContext("uuid-old", "1.1.1.1"), nil, nil)
	s.connTracker.RoutedConnection(context.Background(), stayConn, testInboundContext("uuid-stay", "2.2.2.2"), nil, nil)

	next := []model.UserSpec{{ID: 2, UUID: "uuid-stay"}, {ID: 3, UUID: "uuid-new"}}
	s.closeUUIDsLocked(kernel.RemovedUUIDs(s.users, next))

	if !oldConn.closed {
		t.Fatal("expected the UUID missing from the new user set to be force-closed")
	}
	if stayConn.closed {
		t.Fatal("expected a UUID that is still authorized to stay open")
	}
	if err := s.CloseUserConnections(context.Background(), "uuid-stay"); err != nil {
		t.Fatalf("CloseUserConnections: %v", err)
	}
	if !stayConn.closed {
		t.Fatal("CloseUserConnections did not close the tracked connection")
	}
}

type testPacketConn struct {
	closed bool
}

func (c *testPacketConn) ReadPacket(*buf.Buffer) (singM.Socksaddr, error) {
	return singM.Socksaddr{}, errors.New("eof")
}
func (c *testPacketConn) WritePacket(*buf.Buffer, singM.Socksaddr) error { return nil }
func (c *testPacketConn) Close() error                                   { c.closed = true; return nil }
func (c *testPacketConn) LocalAddr() net.Addr                            { return &net.UDPAddr{} }
func (c *testPacketConn) SetDeadline(time.Time) error                    { return nil }
func (c *testPacketConn) SetReadDeadline(time.Time) error                { return nil }
func (c *testPacketConn) SetWriteDeadline(time.Time) error               { return nil }

func TestConnTrackerCloseByUUIDClosesUDP(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-gone": 1})
	base := &testPacketConn{}
	wrapped := tracker.RoutedPacketConnection(context.Background(), base, testInboundContext("uuid-gone", "1.1.1.1"), nil, nil)
	if _, ok := wrapped.(N.PacketConn); !ok {
		t.Fatalf("wrapped type = %T, want PacketConn", wrapped)
	}
	if got := tracker.CloseByUUID("uuid-gone"); got != 1 {
		t.Fatalf("CloseByUUID() = %d, want 1", got)
	}
	if !base.closed {
		t.Fatal("expected the UDP flow for the removed UUID to close")
	}
}

func TestConnTrackerCloseByIDClosesTrackedConnection(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	base := &testConn{}
	wrapped := tracker.RoutedConnection(context.Background(), base, testInboundContext("uuid-1", "1.1.1.1"), nil, nil)
	tracked, ok := wrapped.(*trackedConn)
	if !ok {
		t.Fatalf("wrapped type = %T, want *trackedConn", wrapped)
	}
	if !tracker.CloseByID(tracked.connID) {
		t.Fatal("CloseByID() = false, want true")
	}
	if !base.closed {
		t.Fatal("expected underlying connection to be closed")
	}
}

func TestConnTrackerRateLimitHonorsContextCancellation(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"uuid-1": 1})
	tracker.SetSpeedLimitFunc(func(uuid string) *rate.Limiter {
		if uuid != "uuid-1" {
			return nil
		}
		return rate.NewLimiter(rate.Limit(1), 1)
	})

	readCtx, cancelRead := context.WithCancel(context.Background())
	readBase := &testConn{reads: [][]byte{[]byte("a"), []byte("a")}}
	readTracked := tracker.RoutedConnection(readCtx, readBase, testInboundContext("uuid-1", "1.1.1.1"), nil, nil).(*trackedConn)
	buf := make([]byte, 8)
	if n, err := readTracked.Read(buf); err != nil || n != 1 {
		t.Fatalf("first Read() = (%d, %v), want (1, nil)", n, err)
	}
	cancelRead()
	if n, err := readTracked.Read(buf); !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("second Read() = (%d, %v), want (1, context canceled)", n, err)
	}

	writeCtx, cancelWrite := context.WithCancel(context.Background())
	writeBase := &testConn{}
	writeTracked := tracker.RoutedConnection(writeCtx, writeBase, testInboundContext("uuid-1", "1.1.1.2"), nil, nil).(*trackedConn)
	if n, err := writeTracked.Write([]byte("a")); err != nil || n != 1 {
		t.Fatalf("first Write() = (%d, %v), want (1, nil)", n, err)
	}
	cancelWrite()
	if n, err := writeTracked.Write([]byte("a")); !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("second Write() = (%d, %v), want (0, context canceled)", n, err)
	}
}
