package main

import (
	"bytes"
	"sync"
)

// syncBuf is a goroutine-safe io.Writer wrapper around bytes.Buffer, used by the
// serve tests where the async serve goroutine writes to a buffer the test reads
// concurrently. Without the mutex the tests would race under -race.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p under the lock.
func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String returns a snapshot of the accumulated bytes under the lock.
func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
