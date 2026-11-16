package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
)

// ErrClosed is returned by Write after Close.
var ErrClosed = errors.New("store: closed")

type writeReq struct {
	ctx  context.Context
	fn   func(*sql.Tx) error
	raw  func(context.Context, *sql.DB) error // instead of fn: runs outside a transaction (VACUUM INTO)
	done chan error
}

// writer serialises all writes on one goroutine and one connection.
type writer struct {
	db   *sql.DB
	reqs chan writeReq

	mu     sync.RWMutex // held for reading while sending to reqs
	closed bool
	exited chan struct{}
}

func newWriter(db *sql.DB) *writer {
	w := &writer{db: db, reqs: make(chan writeReq, 64), exited: make(chan struct{})}
	go w.loop()
	return w
}

func (w *writer) loop() {
	defer close(w.exited)
	// reqs is closed only after all senders are done, so ranging over it
	// drains every queued request before exit.
	for r := range w.reqs {
		r.done <- w.exec(r)
	}
}

func (w *writer) exec(r writeReq) (err error) {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.raw != nil {
		return r.raw(r.ctx, w.db)
	}
	tx, err := w.db.BeginTx(r.ctx, nil)
	if err != nil {
		return fmt.Errorf("store begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("store write panic: %v", p)
		}
		if err != nil {
			tx.Rollback()
		}
	}()
	if err := r.fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store commit: %w", err)
	}
	return nil
}

func (w *writer) do(ctx context.Context, fn func(*sql.Tx) error) error {
	return w.send(writeReq{ctx: ctx, fn: fn, done: make(chan error, 1)})
}

func (w *writer) send(r writeReq) error {
	ctx := r.ctx

	w.mu.RLock()
	if w.closed {
		w.mu.RUnlock()
		return ErrClosed
	}
	select {
	case w.reqs <- r:
	case <-ctx.Done():
		w.mu.RUnlock()
		return ctx.Err()
	}
	w.mu.RUnlock()

	// Once queued, the request always gets an answer: the loop drains the
	// queue before exiting, and exec itself honours ctx.
	return <-r.done
}

func (w *writer) close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.reqs)
	w.mu.Unlock()

	<-w.exited
	return w.db.Close()
}
