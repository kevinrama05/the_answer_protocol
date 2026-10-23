// Command gui is the TAP graphical client. It serves a single-page web UI on
// localhost and bridges it to a TAP server: every browser session owns one
// TCP connection, server lines are streamed with Server-Sent Events and
// commands are POSTed back. The bridge forwards lines verbatim, so the UI
// speaks plain RFC 42TAP and works with any compliant server.
package main

import (
	"bufio"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

type bridge struct {
	defaultHost string
	defaultPort string

	mu       sync.Mutex
	sessions map[string]net.Conn
}

func main() {
	host := flag.String("host", "localhost", "default TAP server host")
	port := flag.Int("port", 4242, "default TAP server port")
	listen := flag.String("listen", "127.0.0.1:8080", "address of the local web UI")
	open := flag.Bool("open", true, "open the UI in the default browser")
	flag.Parse()

	b := &bridge{defaultHost: *host, defaultPort: fmt.Sprint(*port), sessions: map[string]net.Conn{}}
	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/config", b.config)
	mux.HandleFunc("/events", b.events)
	mux.HandleFunc("/send", b.send)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	uiURL := "http://" + ln.Addr().String() + "/"
	log.Printf("TAP GUI ready on %s (default server %s:%d)", uiURL, *host, *port)
	if *open {
		go openBrowser(uiURL)
	}
	srv := &http.Server{Handler: sameOrigin(mux), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.Serve(ln))
}

// sameOrigin rejects cross-site requests so that other web pages cannot
// drive the bridge.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("open %s in your browser (%v)", u, err)
		return
	}
	go func() { _ = cmd.Wait() }()
}

func (b *bridge) config(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"host":%q,"port":%q}`, b.defaultHost, b.defaultPort)
}

func newID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// events opens a TCP connection to the TAP server and streams its lines.
func (b *bridge) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		host = b.defaultHost
	}
	port := r.URL.Query().Get("port")
	if port == "" {
		port = b.defaultPort
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 5*time.Second)
	if err != nil {
		fmt.Fprintf(w, "event: failure\ndata: %s\n\n", oneLine(err.Error()))
		fl.Flush()
		return
	}
	sid := newID()
	b.mu.Lock()
	b.sessions[sid] = conn
	b.mu.Unlock()
	log.Printf("session %s connected to %s", sid[:8], conn.RemoteAddr())
	defer func() {
		b.mu.Lock()
		delete(b.sessions, sid)
		b.mu.Unlock()
		conn.Close()
		log.Printf("session %s closed", sid[:8])
	}()

	fmt.Fprintf(w, "retry: 86400000\nevent: session\ndata: %s\n\n", sid)
	fl.Flush()

	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(conn)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- strings.TrimRight(sc.Text(), "\r")
		}
	}()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case line, ok := <-lines:
			if !ok {
				io.WriteString(w, "event: closed\ndata: server closed the connection\n\n")
				fl.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			fl.Flush()
		}
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// send writes one command line to the session's TAP connection.
func (b *bridge) send(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	b.mu.Lock()
	conn := b.sessions[r.URL.Query().Get("sid")]
	b.mu.Unlock()
	if conn == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	line := oneLine(strings.TrimSpace(string(body)))
	if line == "" {
		http.Error(w, "empty command", http.StatusBadRequest)
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, line+"\n"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
