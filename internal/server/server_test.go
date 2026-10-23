package server

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"tap/internal/game"
	"tap/internal/world"
)

func startServer(t *testing.T, cfg Config) string {
	t.Helper()
	w, _, err := world.Load("../../world/world.json")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := New(game.New(w, log, game.Options{Seed: 1}), log, cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { ln.Close(); srv.Shutdown() })
	return ln.Addr().String()
}

type conn struct {
	t *testing.T
	c net.Conn
	r *bufio.Reader
}

func dial(t *testing.T, addr string) *conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &conn{t: t, c: c, r: bufio.NewReader(c)}
}

func (c *conn) write(s string) {
	c.t.Helper()
	if _, err := io.WriteString(c.c, s); err != nil {
		c.t.Fatal(err)
	}
}

func (c *conn) read() string {
	c.t.Helper()
	_ = c.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("read: %v (partial %q)", err, line)
	}
	return strings.TrimSuffix(line, "\n")
}

func (c *conn) expect(want string) {
	c.t.Helper()
	if got := c.read(); got != want {
		c.t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFramingAndAuth(t *testing.T) {
	addr := startServer(t, DefaultConfig())
	c := dial(t, addr)
	c.expect("OK hello proto=1")
	c.write("LOOK\n")
	c.expect("ERR 102 NOT_AUTHENTICATED")

	// A command split across several TCP writes.
	c.write("CONN")
	time.Sleep(20 * time.Millisecond)
	c.write("ECT ali")
	time.Sleep(20 * time.Millisecond)
	c.write("ce\r\n")
	c.expect("OK connected")
	c.expect("EVT STATS players=1")

	// Several commands coalesced in one write.
	c.write("who\nMOVE north\nCONNECT bob\n")
	c.expect("OK players=1")
	c.expect("OK room=loc.watch_post")
	c.expect("ERR 103 ALREADY_AUTHENTICATED")

	c.write(strings.Repeat("x", 3000) + "\nWHO\n")
	c.expect("ERR 104 LINE_TOO_LONG")
	c.expect("OK players=1")

	c.write("CHAT ROOM \xff\xfe\n")
	c.expect("ERR 106 INVALID_ENCODING")
	c.write("DANCE\n")
	c.expect("ERR 100 UNKNOWN_COMMAND")

	c.write("QUIT\n")
	c.expect("OK bye")
	_ = c.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.r.ReadString('\n'); err != io.EOF {
		t.Fatalf("connection not closed after QUIT: %v", err)
	}
}

func TestAbruptDisconnectBroadcastsLeave(t *testing.T) {
	addr := startServer(t, DefaultConfig())
	a := dial(t, addr)
	a.expect("OK hello proto=1")
	a.write("CONNECT alice\n")
	a.expect("OK connected")
	a.expect("EVT STATS players=1")

	b := dial(t, addr)
	b.expect("OK hello proto=1")
	b.write("CONNECT bob\n")
	b.expect("OK connected")
	b.expect("EVT STATS players=2")
	a.expect("EVT ROOM PRESENCE ENTER bob")
	a.expect("EVT STATS players=2")

	a.c.Close() // no QUIT
	b.expect("EVT ROOM PRESENCE LEAVE alice")
	b.expect("EVT STATS players=1")
	b.write("CHAT GLOBAL still here\n")
	b.expect("OK")
	b.expect("EVT GLOBAL CHAT bob still here")

	// The name is free again.
	c := dial(t, addr)
	c.expect("OK hello proto=1")
	c.write("CONNECT alice\n")
	c.expect("OK connected")
}

func TestRateLimitAndRapidConnections(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Burst = 3
	cfg.Rate = 0.001
	cfg.ConnPerWindow = 3
	addr := startServer(t, cfg)

	c := dial(t, addr)
	c.expect("OK hello proto=1")
	c.write("WHO\nWHO\nWHO\nWHO\n")
	c.expect("ERR 102 NOT_AUTHENTICATED")
	c.expect("ERR 102 NOT_AUTHENTICATED")
	c.expect("ERR 102 NOT_AUTHENTICATED")
	c.expect("ERR 105 RATE_LIMITED")

	dial(t, addr).expect("OK hello proto=1")
	dial(t, addr).expect("OK hello proto=1")
	dial(t, addr).expect("ERR 900 CONNECTION_FAILED")
}
