package server

import (
	"io"
	"sync"
)

// AsyncWriter decouples log output from request handling: Write copies the
// record into a buffered channel and a background goroutine performs the
// actual (possibly slow) I/O.
type AsyncWriter struct {
	ch   chan []byte
	dst  io.Writer
	done chan struct{}
	mu   sync.RWMutex
	shut bool
}

// NewAsyncWriter starts the background writer.
func NewAsyncWriter(dst io.Writer, buffer int) *AsyncWriter {
	a := &AsyncWriter{ch: make(chan []byte, buffer), dst: dst, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		for b := range a.ch {
			_, _ = a.dst.Write(b)
		}
	}()
	return a
}

// Write queues a copy of p (or writes directly once closed).
func (a *AsyncWriter) Write(p []byte) (int, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.shut {
		return a.dst.Write(p)
	}
	a.ch <- append([]byte(nil), p...)
	return len(p), nil
}

// Close flushes pending records and stops the writer.
func (a *AsyncWriter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.shut {
		a.shut = true
		close(a.ch)
		<-a.done
	}
	return nil
}
