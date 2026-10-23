// Command server runs the TAP (RFC 42TAP) game server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tap/internal/game"
	"tap/internal/server"
	"tap/internal/world"
)

func main() {
	addr := flag.String("addr", ":4242", "listen address")
	worldPath := flag.String("world", "world/world.json", "world definition (JSON)")
	logFile := flag.String("log-file", "", "also append JSON logs to this file")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	respawn := flag.Duration("respawn", game.DefaultRespawn, "enemy respawn delay")
	checkOnly := flag.Bool("check", false, "validate the world file and exit")
	flag.Parse()

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "invalid -log-level:", *logLevel)
		os.Exit(2)
	}

	var out io.Writer = os.Stderr
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cannot open log file:", err)
			os.Exit(1)
		}
		defer f.Close()
		out = io.MultiWriter(os.Stderr, f)
	}
	aw := server.NewAsyncWriter(out, 8192)
	log := slog.New(slog.NewJSONHandler(aw, &slog.HandlerOptions{Level: level}))

	os.Exit(run(log, aw, *addr, *worldPath, *respawn, *checkOnly))
}

func run(log *slog.Logger, aw *server.AsyncWriter, addr, worldPath string, respawn time.Duration, checkOnly bool) int {
	defer aw.Close()

	w, warns, err := world.Load(worldPath)
	for _, wmsg := range warns {
		log.Warn("world warning", "type", "world", "detail", wmsg)
	}
	if err != nil {
		var verr *world.ValidationError
		if errors.As(err, &verr) {
			for _, p := range verr.Problems {
				log.Error("world error", "type", "world", "detail", p)
			}
		}
		log.Error("cannot load world", "type", "startup", "path", worldPath, "error", firstLine(err.Error()))
		return 1
	}
	log.Info("world loaded", "type", "startup", "world", w.ID, "rooms", len(w.Rooms), "items", len(w.Items),
		"npcs", len(w.NPCs), "quests", len(w.Quests), "warnings", len(warns))
	if checkOnly {
		return 0
	}

	g := game.New(w, log, game.Options{RespawnDelay: respawn})
	srv := server.New(g, log, server.DefaultConfig())

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("cannot listen", "type", "startup", "addr", addr, "error", err.Error())
		return 1
	}
	log.Info("server listening", "type", "startup", "addr", ln.Addr().String())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Info("shutting down", "type", "shutdown", "signal", s.String())
		_ = ln.Close()
	}()

	if err := srv.Serve(ln); err != nil {
		log.Error("serve failed", "type", "shutdown", "error", err.Error())
	}
	srv.Shutdown()
	log.Info("server stopped", "type", "shutdown")
	return 0
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
