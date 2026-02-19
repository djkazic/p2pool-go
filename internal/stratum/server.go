package stratum

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	// connReadTimeout is how long to wait for data from a miner before
	// considering the connection dead. Reset after each successful read.
	connReadTimeout = 5 * time.Minute

	// tcpKeepAliveInterval is the TCP keepalive probe interval.
	tcpKeepAliveInterval = 30 * time.Second
)

// Server is a Stratum v1 mining server.
type Server struct {
	listener net.Listener
	logger   *zap.Logger

	sessions   map[string]*Session
	sessionsMu sync.RWMutex

	submitCh chan *ShareSubmission

	// Extranonce allocation
	extranonceCounter atomic.Uint64
	extranonce2Size   int

	// Initial difficulty for new miners (vardiff adjusts from here)
	startDifficulty float64

	// Current job
	currentJob   *Job
	currentJobMu sync.RWMutex

	maxSessions int

	httpHandler http.Handler

	cancel context.CancelFunc
}

// NewServer creates a new Stratum server.
func NewServer(startDifficulty float64, logger *zap.Logger) *Server {
	return &Server{
		logger:          logger,
		sessions:        make(map[string]*Session),
		submitCh:        make(chan *ShareSubmission, 256),
		extranonce2Size: 4,
		startDifficulty: startDifficulty,
		maxSessions:     1000,
	}
}

// Start begins listening on the given address.
func (s *Server) Start(addr string) error {
	var err error
	s.listener, err = net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	s.logger.Info("stratum server listening", zap.String("addr", addr))

	go s.acceptLoop(ctx)
	return nil
}

// Stop stops the server.
func (s *Server) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		s.listener.Close()
	}

	s.sessionsMu.Lock()
	for _, session := range s.sessions {
		session.Close()
	}
	s.sessions = make(map[string]*Session)
	s.sessionsMu.Unlock()

	return nil
}

// SubmitChannel returns the channel of share submissions.
func (s *Server) SubmitChannel() <-chan *ShareSubmission {
	return s.submitCh
}

// BroadcastJob sends a new job to all connected miners.
func (s *Server) BroadcastJob(job *Job) {
	s.currentJobMu.Lock()
	s.currentJob = job
	s.currentJobMu.Unlock()

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	for _, session := range s.sessions {
		if session.State == StateAuthorized {
			if err := session.NotifyJob(job); err != nil {
				s.logger.Warn("failed to notify miner", zap.String("session", session.ID), zap.Error(err))
			}
		}
	}
}

// SessionCount returns the number of active sessions.
func (s *Server) SessionCount() int {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	return len(s.sessions)
}

// SessionInfo holds a snapshot of per-session info for the dashboard.
type SessionInfo struct {
	WorkerName  string
	Difficulty  float64
	ConnectedAt time.Time
}

// MinerStats returns a snapshot of all authorized sessions.
func (s *Server) MinerStats() []SessionInfo {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	var out []SessionInfo
	for _, sess := range s.sessions {
		if sess.State == StateAuthorized {
			out = append(out, SessionInfo{
				WorkerName:  sess.WorkerName,
				Difficulty:  sess.Vardiff.Difficulty(),
				ConnectedAt: sess.ConnectedAt,
			})
		}
	}
	return out
}

// SetHTTPHandler sets an HTTP handler for non-stratum connections.
// HTTP requests are detected by peeking the first byte of each connection.
func (s *Server) SetHTTPHandler(h http.Handler) {
	s.httpHandler = h
}

func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				s.logger.Error("accept error", zap.Error(err))
				continue
			}
		}

		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	// Peek the first byte to determine the protocol.
	// Stratum (JSON-RPC) always starts with '{'.
	// HTTP requests start with a letter (G for GET, P for POST, etc.).
	buf := make([]byte, 1)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{}) // clear deadline

	if buf[0] != '{' && s.httpHandler != nil {
		s.serveHTTP(conn, buf[0])
		return
	}

	// Stratum: wrap conn so the peeked byte is read first
	prefixed := &prefixConn{Conn: conn, prefix: buf}

	// Check session capacity
	s.sessionsMu.RLock()
	atCapacity := len(s.sessions) >= s.maxSessions
	s.sessionsMu.RUnlock()
	if atCapacity {
		s.logger.Warn("stratum session limit reached, rejecting connection",
			zap.String("remote", conn.RemoteAddr().String()),
			zap.Int("max_sessions", s.maxSessions),
		)
		conn.Close()
		return
	}

	// Enable TCP keepalive to detect dead connections
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(tcpKeepAliveInterval)
	}

	// Allocate unique extranonce1
	id := s.extranonceCounter.Add(1)
	extranonce1 := fmt.Sprintf("%08x", id)
	sessionID := extranonce1

	codec := NewCodec(prefixed)
	session := NewSession(sessionID, codec, extranonce1, s.extranonce2Size, s.startDifficulty, s.submitCh, s.logger)

	s.sessionsMu.Lock()
	s.sessions[sessionID] = session
	s.sessionsMu.Unlock()

	s.logger.Info("miner connected", zap.String("session", sessionID), zap.String("remote", conn.RemoteAddr().String()))

	defer func() {
		s.sessionsMu.Lock()
		delete(s.sessions, sessionID)
		s.sessionsMu.Unlock()
		session.Close()
		s.logger.Info("miner disconnected", zap.String("session", sessionID))
	}()

	initialJobSent := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Set read deadline so we detect dead connections instead of blocking forever.
		// The miner should be submitting shares or keepalive messages regularly.
		conn.SetReadDeadline(time.Now().Add(connReadTimeout))

		req, err := codec.ReadRequest()
		if err != nil {
			s.logger.Debug("read error", zap.String("session", sessionID), zap.Error(err))
			return
		}

		if err := session.HandleRequest(req); err != nil {
			s.logger.Error("handle error", zap.String("session", sessionID), zap.Error(err))
			return
		}

		// Once a miner is authorized, immediately send the current job
		// so it can start mining without waiting for the next template poll.
		if !initialJobSent && session.State == StateAuthorized {
			s.currentJobMu.RLock()
			job := s.currentJob
			s.currentJobMu.RUnlock()
			if job != nil {
				if err := session.NotifyJob(job); err != nil {
					s.logger.Warn("failed to send initial job", zap.String("session", sessionID), zap.Error(err))
				} else {
					s.logger.Debug("sent initial job to miner", zap.String("session", sessionID), zap.String("job", job.ID))
				}
			} else {
				s.logger.Warn("no job available for newly authorized miner", zap.String("session", sessionID))
			}
			initialJobSent = true
		}
	}
}

// serveHTTP serves a single HTTP connection by wrapping it in a one-shot listener.
func (s *Server) serveHTTP(conn net.Conn, firstByte byte) {
	prefixed := &prefixConn{Conn: conn, prefix: []byte{firstByte}}
	listener := &singleConnListener{conn: prefixed, done: make(chan struct{})}
	srv := &http.Server{
		Handler:      s.httpHandler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  5 * time.Second,
	}
	srv.Serve(listener)
}

// prefixConn wraps a net.Conn and prepends previously-peeked bytes.
type prefixConn struct {
	net.Conn
	prefix []byte
	read   bool
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if !c.read && len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		if len(c.prefix) == 0 {
			c.read = true
		}
		return n, nil
	}
	return c.Conn.Read(p)
}

// singleConnListener accepts exactly one connection then blocks until closed.
type singleConnListener struct {
	conn     net.Conn
	done     chan struct{}
	accepted sync.Once
	closed   sync.Once
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.accepted.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *singleConnListener) Close() error {
	l.closed.Do(func() { close(l.done) })
	return nil
}

func (l *singleConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
