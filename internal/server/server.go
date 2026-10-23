// Package server is the TCP front-end of TAP: it frames lines, enforces
// limits, logs traffic and hands commands to the game.
package server

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"tap/internal/game"
)

// Config holds the network limits.
type Config struct {
	MaxConns      int           // simultaneous connections
	MaxLine       int           // bytes per line, newline excluded
	Rate          float64       // sustained commands per second per client
	Burst         int           // command burst per client
	FloodKick     int           // rate-limited commands before disconnect
	ConnWindow    time.Duration // window for rapid-connection detection
	ConnPerWindow int           // connections per IP per window
	SendQueue     int           // queued outgoing lines per client
	WriteTimeout  time.Duration
}

// DefaultConfig returns the limits recommended by RFC 42TAP section 9.4.
func DefaultConfig() Config {
	return Config{
		MaxConns:      128,
		MaxLine:       1024,
		Rate:          8,
		Burst:         20,
		FloodKick:     200,
		ConnWindow:    10 * time.Second,
		ConnPerWindow: 10,
		SendQueue:     512,
		WriteTimeout:  10 * time.Second,
	}
}

// Server accepts TAP clients.
type Server struct {
	cfg  Config
	game *game.Game
	log  *slog.Logger

	mu     sync.Mutex
	conns  map[*client]struct{}
	ipHits map[string][]time.Time
	wg     sync.WaitGroup
}

// New creates a server bound to g.
func New(g *game.Game, log *slog.Logger, cfg Config) *Server {
	return &Server{
		cfg:    cfg,
		game:   g,
		log:    log,
		conns:  make(map[*client]struct{}),
		ipHits: make(map[string][]time.Time),
	}
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.log.Error("accept failed", "type", "network", "error", err.Error())
			time.Sleep(50 * time.Millisecond)
			continue
		}
		s.admit(conn)
	}
}

// Shutdown closes every client and waits for their goroutines.
func (s *Server) Shutdown() {
	s.mu.Lock()
	for c := range s.conns {
		c.close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func hostOf(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

func (s *Server) admit(conn net.Conn) {
	ip := hostOf(conn.RemoteAddr())
	now := time.Now()

	s.mu.Lock()
	hits := s.ipHits[ip][:0]
	for _, t := range s.ipHits[ip] {
		if now.Sub(t) < s.cfg.ConnWindow {
			hits = append(hits, t)
		}
	}
	hits = append(hits, now)
	s.ipHits[ip] = hits
	reason := ""
	switch {
	case len(hits) > s.cfg.ConnPerWindow:
		reason = "rapid_connections"
	case len(s.conns) >= s.cfg.MaxConns:
		reason = "server_full"
	}
	var c *client
	if reason == "" {
		c = newClient(conn, ip, s.cfg, s.log)
		s.conns[c] = struct{}{}
		s.wg.Add(1)
	}
	s.mu.Unlock()

	if reason != "" {
		s.log.Warn("connection refused", "type", "abuse", "ip", ip, "remote", conn.RemoteAddr().String(),
			"reason", reason, "attempts_in_window", len(hits))
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(conn, game.ErrConnectionFail+"\n")
		_ = conn.Close()
		return
	}
	s.log.Info("client connected", "type", "connection", "ip", ip, "remote", conn.RemoteAddr().String())
	go s.handle(c)
}

func (s *Server) handle(c *client) {
	defer s.wg.Done()
	started := time.Now()
	go c.writeLoop()

	var player *game.Player
	reason := "client_closed"
	name := func() string {
		if player != nil {
			return player.Name
		}
		return ""
	}
	reply := func(cmd, line string) {
		c.Send(line)
		game.LogReply(s.log, name(), c.ip, cmd, line)
	}

	c.Send("OK hello proto=1")
	r := bufio.NewReaderSize(c.conn, 4096)
	tokens := float64(s.cfg.Burst)
	last := time.Now()
	flooded := 0

loop:
	for {
		line, tooLong, err := readLine(r, s.cfg.MaxLine)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				reason = "read_error: " + err.Error()
			}
			if c.isClosed() && reason == "client_closed" {
				reason = "server_closed"
			}
			break
		}
		if tooLong {
			s.log.Warn("line too long", "type", "abuse", "player", name(), "ip", c.ip, "limit", s.cfg.MaxLine)
			reply("", game.ErrLineTooLong)
			continue
		}
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !utf8.ValidString(line) {
			reply("", game.ErrInvalidEncoding)
			continue
		}

		now := time.Now()
		tokens = min(float64(s.cfg.Burst), tokens+now.Sub(last).Seconds()*s.cfg.Rate)
		last = now
		if tokens < 1 {
			flooded++
			if flooded == 1 || flooded%50 == 0 {
				s.log.Warn("command flood", "type", "abuse", "player", name(), "ip", c.ip, "rejected", flooded)
			}
			if flooded >= s.cfg.FloodKick {
				reason = "flood"
				break
			}
			c.Send(game.ErrRateLimited)
			continue
		}
		tokens--
		flooded = 0

		cmd, args := game.SplitCommand(line)
		s.log.Info("command", "type", "command", "player", name(), "ip", c.ip, "command", cmd, "args", truncate(args, 200))

		switch {
		case cmd == "QUIT":
			reply(cmd, "OK bye")
			reason = "quit"
			break loop
		case cmd == "CONNECT":
			if player != nil {
				reply(cmd, game.ErrAlreadyAuth)
				continue
			}
			player = s.game.Connect(args, c, c.ip)
		case player == nil:
			reply(cmd, game.ErrNotAuth)
		default:
			s.game.Handle(player, cmd, args)
		}
	}

	// Remove the player from the world before anyone hears about it.
	if player != nil {
		s.game.Disconnect(player, reason)
	}
	c.closeAfterFlush()
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	s.log.Info("client disconnected", "type", "connection", "player", name(), "ip", c.ip,
		"reason", reason, "duration_ms", time.Since(started).Milliseconds())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// readLine returns the next '\n'-terminated line (without the newline).
// Lines longer than limit are consumed entirely and reported as tooLong, so
// one oversized message never desynchronises the stream.
func readLine(r *bufio.Reader, limit int) (string, bool, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			buf = append(buf, chunk...)
			if len(buf) > limit+1 {
				tooLong = true
				buf = nil
			}
		}
		switch {
		case err == nil:
			if tooLong {
				return "", true, nil
			}
			return string(buf[:len(buf)-1]), false, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return "", false, err
		}
	}
}

// ---------------------------------------------------------------------------

const closeSentinel = "\x00close"

// client is one TCP connection. Send never blocks: lines go to a bounded
// queue drained by writeLoop. A client that cannot keep up is disconnected
// so that broadcasts to everybody else are never stalled.
type client struct {
	conn    net.Conn
	ip      string
	cfg     Config
	log     *slog.Logger
	out     chan string
	closed  chan struct{}
	once    sync.Once
	flushed sync.Once
}

func newClient(conn net.Conn, ip string, cfg Config, log *slog.Logger) *client {
	return &client{
		conn:   conn,
		ip:     ip,
		cfg:    cfg,
		log:    log,
		out:    make(chan string, cfg.SendQueue),
		closed: make(chan struct{}),
	}
}

// Send implements game.Sink.
func (c *client) Send(line string) {
	select {
	case <-c.closed:
		return
	default:
	}
	select {
	case c.out <- line:
	default:
		c.log.Error("send queue full, dropping client", "type", "network", "ip", c.ip, "code", 901, "error", "SEND_FAILED")
		c.close()
	}
}

func (c *client) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *client) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.conn.Close()
	})
}

// closeAfterFlush lets the writer send what is queued (e.g. "OK bye") and
// then closes the connection.
func (c *client) closeAfterFlush() {
	c.flushed.Do(func() {
		select {
		case c.out <- closeSentinel:
		default:
			c.close()
		}
	})
}

func (c *client) writeLoop() {
	w := bufio.NewWriter(c.conn)
	for {
		select {
		case <-c.closed:
			return
		case line := <-c.out:
			if line == closeSentinel {
				_ = w.Flush()
				c.close()
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
			_, err := w.WriteString(line + "\n")
			if err == nil && len(c.out) == 0 {
				err = w.Flush()
			}
			if err != nil {
				if !c.isClosed() {
					c.log.Error("send failed", "type", "network", "ip", c.ip, "code", 901, "error", err.Error())
				}
				c.close()
				return
			}
		}
	}
}
