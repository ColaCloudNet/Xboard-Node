package anytls

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"sync"

	"github.com/anytls/sing-anytls/padding"
	"github.com/anytls/sing-anytls/session"
	"github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type Service struct {
	// userMu guards users. Lock order is userMu then sessionMu.
	userMu          sync.RWMutex
	users           map[[32]byte]string
	padding         atomic.TypedValue[*padding.PaddingFactory]
	handler         N.TCPConnectionHandlerEx
	fallbackHandler N.TCPConnectionHandlerEx
	logger          logger.ContextLogger

	// sessions indexes authenticated server sessions by the password hash that
	// opened them. anytls checks that hash once, at handshake, then multiplexes
	// streams on the session without looking at the password again.
	sessionMu sync.Mutex
	sessions  map[*session.Session]sessionIdentity
}

// sessionIdentity is the credential a server session authenticated with.
type sessionIdentity struct {
	name         string
	passwordHash [32]byte
}

type ServiceConfig struct {
	PaddingScheme   []byte
	Users           []User
	Handler         N.TCPConnectionHandlerEx
	FallbackHandler N.TCPConnectionHandlerEx
	Logger          logger.ContextLogger
}

type User struct {
	Name     string
	Password string
}

func NewService(config ServiceConfig) (*Service, error) {
	service := &Service{
		handler:         config.Handler,
		fallbackHandler: config.FallbackHandler,
		logger:          config.Logger,
		users:           make(map[[32]byte]string),
		sessions:        make(map[*session.Session]sessionIdentity),
	}

	if service.handler == nil || service.logger == nil {
		return nil, os.ErrInvalid
	}

	for _, user := range config.Users {
		service.users[sha256.Sum256([]byte(user.Password))] = user.Name
	}

	if !padding.UpdatePaddingScheme(config.PaddingScheme, &service.padding) {
		return nil, errors.New("incorrect padding scheme format")
	}

	return service, nil
}

func (s *Service) UpdateUsers(users []User) {
	u := make(map[[32]byte]string, len(users))
	for _, user := range users {
		u[sha256.Sum256([]byte(user.Password))] = user.Name
	}

	// Hold userMu across the session scan so a handshake that already passed
	// the password check cannot register itself after that password is gone.
	s.userMu.Lock()
	s.users = u
	removed := s.takeRemovedSessionsLocked(u)
	s.userMu.Unlock()

	// Close outside the locks: Session.Close shuts the connection and every
	// multiplexed stream, and must not run while sessionMu is held.
	for _, userSession := range removed {
		_ = userSession.Close()
	}
}

// takeRemovedSessionsLocked returns sessions whose password is no longer
// present (or is now bound to a different name) and drops them from the index.
// Caller must hold s.userMu.
func (s *Service) takeRemovedSessionsLocked(valid map[[32]byte]string) []*session.Session {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	var removed []*session.Session
	for userSession, id := range s.sessions {
		name, ok := valid[id.passwordHash]
		if ok && name == id.name {
			continue
		}
		removed = append(removed, userSession)
		delete(s.sessions, userSession)
	}
	return removed
}

// trackSession registers an authenticated session when its password is still
// in the user table. It returns false when UpdateUsers has already dropped
// that password, in which case the caller must close the session.
func (s *Service) trackSession(userSession *session.Session, user string, passwordHash [32]byte) bool {
	s.userMu.RLock()
	defer s.userMu.RUnlock()
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	name, ok := s.users[passwordHash]
	if !ok || name != user {
		return false
	}
	s.sessions[userSession] = sessionIdentity{name: user, passwordHash: passwordHash}
	return true
}

func (s *Service) untrackSession(userSession *session.Session) {
	s.sessionMu.Lock()
	delete(s.sessions, userSession)
	s.sessionMu.Unlock()
}

// NewConnection `conn` should be plaintext
func (s *Service) NewConnection(ctx context.Context, conn net.Conn, source M.Socksaddr, onClose N.CloseHandlerFunc) error {
	b := buf.NewPacket()
	defer b.Release()

	n, err := b.ReadOnceFrom(conn)
	if err != nil {
		return err
	}
	conn = bufio.NewCachedConn(conn, b)

	by, err := b.ReadBytes(32)
	if err != nil {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, err, onClose)
	}
	var passwordSha256 [32]byte
	copy(passwordSha256[:], by)
	s.userMu.RLock()
	user, loaded := s.users[passwordSha256]
	s.userMu.RUnlock()
	if !loaded {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, E.New("unknown user password"), onClose)
	}
	ctx = auth.ContextWithUser(ctx, user)
	by, err = b.ReadBytes(2)
	if err != nil {
		b.Resize(0, n)
		return s.fallback(ctx, conn, source, E.Extend(err, "read padding length"), onClose)
	}
	paddingLen := binary.BigEndian.Uint16(by)
	if paddingLen > 0 {
		_, err = b.ReadBytes(int(paddingLen))
		if err != nil {
			b.Resize(0, n)
			return s.fallback(ctx, conn, source, E.Extend(err, "read padding"), onClose)
		}
	}

	userSession := session.NewServerSession(conn, func(stream *session.Stream) {
		destination, err := M.SocksaddrSerializer.ReadAddrPort(stream)
		if err != nil {
			s.logger.ErrorContext(ctx, "ReadAddrPort:", err)
			return
		}

		s.handler.NewConnectionEx(ctx, stream, source, destination, onClose)
	}, &s.padding, s.logger)
	if !s.trackSession(userSession, user, passwordSha256) {
		_ = userSession.Close()
		return E.New("user removed during handshake")
	}
	defer s.untrackSession(userSession)
	userSession.Run()
	userSession.Close()
	return nil
}

func (s *Service) fallback(ctx context.Context, conn net.Conn, source M.Socksaddr, err error, onClose N.CloseHandlerFunc) error {
	if s.fallbackHandler == nil {
		return E.Extend(err, "fallback disabled")
	}
	s.fallbackHandler.NewConnectionEx(ctx, conn, source, M.Socksaddr{}, onClose)
	return nil
}
