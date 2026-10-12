package protocol

import (
	"testing"
	"time"
)

func TestServerUnderlayIdleReclaimClosesEmptyUnderlay(t *testing.T) {
	prev := ServerUnderlayIdleTimeout
	ServerUnderlayIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { ServerUnderlayIdleTimeout = prev })

	mux := NewMux(false)
	// Do not mux.Close() — that closes underlays via StreamUnderlay.Close needing a conn.
	// Stop maintenance by closing done channel path: call Close only after removing underlays.
	t.Cleanup(func() {
		mux.mu.Lock()
		mux.underlays = nil
		mux.mu.Unlock()
		_ = mux.Close()
	})

	underlay := newBaseUnderlay(false, 1400, nil)
	mux.mu.Lock()
	mux.underlays = append(mux.underlays, underlay)
	mux.mu.Unlock()

	// Simulate last session removed → idle mark (same as RemoveSession path).
	underlay.sessionIdleSinceUnixMicro.Store(time.Now().UnixMicro())
	if underlay.SessionIdleSince().IsZero() {
		t.Fatal("expected SessionIdleSince set")
	}

	mux.mu.Lock()
	mux.cleanUnderlay(false)
	mux.mu.Unlock()
	select {
	case <-underlay.Done():
		t.Fatal("underlay closed before idle timeout")
	default:
	}

	time.Sleep(250 * time.Millisecond)
	mux.mu.Lock()
	mux.cleanUnderlay(false)
	left := len(mux.underlays)
	mux.mu.Unlock()

	select {
	case <-underlay.Done():
	default:
		t.Fatal("underlay not closed after idle timeout")
	}
	if left != 0 {
		t.Fatalf("underlays left=%d, want 0", left)
	}
}

func TestServerUnderlayIdleReclaimRequiresPriorSession(t *testing.T) {
	prev := ServerUnderlayIdleTimeout
	ServerUnderlayIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { ServerUnderlayIdleTimeout = prev })

	mux := NewMux(false)
	t.Cleanup(func() {
		mux.mu.Lock()
		mux.underlays = nil
		mux.mu.Unlock()
		_ = mux.Close()
	})

	underlay := newBaseUnderlay(false, 1400, nil)
	mux.mu.Lock()
	mux.underlays = append(mux.underlays, underlay)
	mux.mu.Unlock()

	time.Sleep(80 * time.Millisecond)
	mux.mu.Lock()
	mux.cleanUnderlay(false)
	left := len(mux.underlays)
	mux.mu.Unlock()
	if left != 1 {
		t.Fatalf("never-used underlay should not reclaim, left=%d", left)
	}
	select {
	case <-underlay.Done():
		t.Fatal("never-used underlay should stay open")
	default:
	}
}

func TestRemoveSessionMarksServerIdleSince(t *testing.T) {
	underlay := newBaseUnderlay(false, 1400, nil)
	session := NewSession(42, false, 1400, nil, nil)
	if err := underlay.AddSession(session, nil); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	session.forwardStateTo(sessionAttached)
	if !underlay.SessionIdleSince().IsZero() {
		t.Fatal("idle since should clear on AddSession")
	}
	if err := underlay.RemoveSession(session); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if underlay.SessionIdleSince().IsZero() {
		t.Fatal("idle since should be set when last server session removed")
	}
}

func TestMarkServerIdleWhenSessionsClosedButStillMapped(t *testing.T) {
	underlay := newBaseUnderlay(false, 1400, nil)
	session := NewSession(7, false, 1400, nil, nil)
	if err := underlay.AddSession(session, nil); err != nil {
		t.Fatalf("AddSession: %v", err)
	}
	session.forwardStateTo(sessionAttached)
	_ = session.Close()
	if underlay.SessionCount() != 1 {
		t.Fatalf("SessionCount=%d, want 1 (still mapped)", underlay.SessionCount())
	}
	underlay.MarkServerIdleIfNoLiveSessions()
	if underlay.SessionIdleSince().IsZero() {
		t.Fatal("expected idle mark when only closed sessions remain")
	}
}
