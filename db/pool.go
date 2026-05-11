// Package db provides database operations for the Shelley AI coding agent.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Pool is an SQLite connection pool.
//
// We deliberately minimize our use of database/sql machinery because
// the semantics do not match SQLite well.
//
// Instead, we choose a single connection to use for writing (because
// SQLite is single-writer) and use the rest as readers.
type Pool struct {
	db      *sql.DB
	writer  chan *sql.Conn
	readers chan *sql.Conn

	hooksMu     sync.RWMutex
	commitHooks []func()
}

// OnCommit registers a callback fired synchronously after each successful Tx
// commit on the goroutine that committed the Tx. Callbacks must not call
// back into pool.Tx and should be quick or hand work off elsewhere.
func (p *Pool) OnCommit(fn func()) {
	p.hooksMu.Lock()
	defer p.hooksMu.Unlock()
	p.commitHooks = append(p.commitHooks, fn)
}

func (p *Pool) fireCommitHooks() {
	p.hooksMu.RLock()
	hooks := p.commitHooks
	p.hooksMu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

func NewPool(dataSourceName string, readerCount int) (*Pool, error) {
	if dataSourceName == ":memory:" {
		return nil, fmt.Errorf(":memory: is not supported (because multiple conns are needed); use a temp file")
	}
	// TODO: a caller could override PRAGMA query_only.
	// Consider opening two *sql.DBs, one configured as read-only,
	// to ensure read-only transactions are always such.
	db, err := sql.Open("sqlite", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("NewPool: %w", err)
	}
	numConns := readerCount + 1
	if err := InitPoolDB(db, numConns); err != nil {
		return nil, fmt.Errorf("NewPool: %w", err)
	}

	var conns []*sql.Conn
	for i := 0; i < numConns; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("NewPool: %w", err)
		}
		conns = append(conns, conn)
	}

	p := &Pool{
		db:      db,
		writer:  make(chan *sql.Conn, 1),
		readers: make(chan *sql.Conn, readerCount),
	}
	p.writer <- conns[0]
	for _, conn := range conns[1:] {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA query_only=1;"); err != nil {
			db.Close()
			return nil, fmt.Errorf("NewPool query_only: %w", err)
		}
		p.readers <- conn
	}

	return p, nil
}

// InitPoolDB fixes the database/sql pool to a set of fixed connections.
func InitPoolDB(db *sql.DB, numConns int) error {
	db.SetMaxIdleConns(numConns)
	db.SetMaxOpenConns(numConns)
	db.SetConnMaxLifetime(-1)
	db.SetConnMaxIdleTime(-1)

	initQueries := []string{
		"PRAGMA journal_mode=wal;",
		"PRAGMA busy_timeout=1000;",
		"PRAGMA foreign_keys=ON;",
	}

	var conns []*sql.Conn
	for i := 0; i < numConns; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			db.Close()
			return fmt.Errorf("InitPoolDB: %w", err)
		}
		for _, q := range initQueries {
			if _, err := conn.ExecContext(context.Background(), q); err != nil {
				db.Close()
				return fmt.Errorf("InitPoolDB %d: %w", i, err)
			}
		}
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			db.Close()
			return fmt.Errorf("InitPoolDB: %w", err)
		}
	}
	return nil
}

func (p *Pool) Close() error {
	return p.db.Close()
}

type ctxKeyType int

// CtxKey is the context value key used to store the current *Tx or *Rx.
// In general this should not be used, plumb the tx directly.
// This code is here is used for an exception: the slog package.
var CtxKey any = ctxKeyType(0)

func checkNoTx(ctx context.Context, typ string) {
	x := ctx.Value(CtxKey)
	if x == nil {
		return
	}
	orig := "unexpected"
	switch x := x.(type) {
	case *Tx:
		orig = "Tx (" + x.caller + ")"
	case *Rx:
		orig = "Rx (" + x.caller + ")"
	}
	panic(typ + " inside " + orig)
}

// Exec executes a single statement outside of a transaction.
// Useful in the rare case of PRAGMAs that cannot execute inside a tx,
// such as PRAGMA wal_checkpoint.
func (p *Pool) Exec(ctx context.Context, query string, args ...interface{}) error {
	checkNoTx(ctx, "Tx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return fmt.Errorf("Pool.Exec: %w", ctx.Err())
	case conn = <-p.writer:
	}
	var err error
	defer func() {
		p.writer <- conn
	}()
	_, err = conn.ExecContext(ctx, query, args...)
	return wrapErr("pool.exec", err)
}

func (p *Pool) Tx(ctx context.Context, fn func(ctx context.Context, tx *Tx) error) error {
	checkNoTx(ctx, "Tx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return fmt.Errorf("Tx: %w", ctx.Err())
	case conn = <-p.writer:
	}

	// If the context is closed, we want BEGIN to succeed and then
	// we roll it back later.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "BEGIN IMMEDIATE;"); err != nil {
		if strings.Contains(err.Error(), "SQLITE_BUSY") {
			p.writer <- conn
			return fmt.Errorf("Tx begin: %w", err)
		}
		// unrecoverable error, this will lock everything up
		return fmt.Errorf("Tx LEAK %w", err)
	}
	tx := &Tx{
		Rx:  &Rx{conn: conn, p: p, caller: callerOfCaller(1)},
		Now: time.Now(),
	}
	tx.ctx = context.WithValue(ctx, CtxKey, tx)

	var err error
	committed := false
	defer func() {
		if err == nil {
			_, err = tx.conn.ExecContext(tx.ctx, "COMMIT;")
			if err != nil {
				err = fmt.Errorf("Tx: commit: %w", err)
			} else {
				committed = true
			}
		}
		if err != nil {
			err = p.rollback(tx.ctx, "Tx", err, tx.conn)
			// always return conn,
			// either the entire database is closed or the conn is fine.
		}
		tx.p.writer <- conn
		if committed {
			p.fireCommitHooks()
		}
	}()
	if ctxErr := tx.ctx.Err(); ctxErr != nil {
		return ctxErr // fast path for canceled context
	}
	err = fn(tx.ctx, tx)

	return err
}

func (p *Pool) Rx(ctx context.Context, fn func(ctx context.Context, rx *Rx) error) error {
	checkNoTx(ctx, "Rx")
	var conn *sql.Conn
	select {
	case <-ctx.Done():
		return ctx.Err()
	case conn = <-p.readers:
	}

	// If the context is closed, we want BEGIN to succeed and then
	// we roll it back later.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "BEGIN;"); err != nil {
		if strings.Contains(err.Error(), "SQLITE_BUSY") {
			p.readers <- conn
			return fmt.Errorf("Rx begin: %w", err)
		}
		// an unrecoverable error, e.g. tx-inside-tx misuse or IOERR
		return fmt.Errorf("Rx LEAK: %w", err)
	}
	rx := &Rx{conn: conn, p: p, caller: callerOfCaller(1)}
	rx.ctx = context.WithValue(ctx, CtxKey, rx)

	var err error
	defer func() {
		err = p.rollback(rx.ctx, "Rx", err, rx.conn)
		// always return conn,
		// either the entire database is closed or the conn is fine.
		rx.p.readers <- conn
	}()
	if ctxErr := rx.ctx.Err(); ctxErr != nil {
		return ctxErr // fast path for canceled context
	}
	err = fn(rx.ctx, rx)
	return err
}

func (p *Pool) rollback(ctx context.Context, txType string, txErr error, conn *sql.Conn) error {
	// Even if the context is cancelled,
	// we still need to rollback to finish up the transaction.
	_, err := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK;")
	if err != nil && !strings.Contains(err.Error(), "no transaction is active") {
		// There are a few cases where an error during a transaction
		// will be reported as a rollback error:
		// 	https://sqlite.org/lang_transaction.html#response_to_errors_within_a_transaction
		// In good operation, we should never see any of these.
		//
		// TODO: confirm this check works on all sqlite drivers.
		if !strings.Contains(err.Error(), "SQLITE_BUSY") {
			conn.Close()
			p.db.Close()
		}
		return fmt.Errorf("%s: %v: rollback failed: %w", txType, txErr, err)
	}
	return txErr
}

type Tx struct {
	*Rx
	Now time.Time
}

func (tx *Tx) Exec(query string, args ...interface{}) (sql.Result, error) {
	res, err := tx.conn.ExecContext(tx.ctx, query, args...)
	return res, wrapErr("exec", err)
}

type Rx struct {
	ctx    context.Context
	conn   *sql.Conn
	p      *Pool
	caller string // for debugging
}

func (rx *Rx) Context() context.Context {
	return rx.ctx
}

func (rx *Rx) Query(query string, args ...interface{}) (*sql.Rows, error) {
	rows, err := rx.conn.QueryContext(rx.ctx, query, args...)
	return rows, wrapErr("query", err)
}

func (rx *Rx) QueryRow(query string, args ...interface{}) *Row {
	rows, err := rx.conn.QueryContext(rx.ctx, query, args...)
	return &Row{err: err, rows: rows}
}

// Conn returns the underlying sql.Conn for use with external libraries like sqlc
func (rx *Rx) Conn() *sql.Conn {
	return rx.conn
}

// Row is equivalent to *sql.Row, but we provide a more useful error.
type Row struct {
	err  error
	rows *sql.Rows
}

func (r *Row) Scan(dest ...any) error {
	if r.err != nil {
		return wrapErr("QueryRow", r.err)
	}

	defer r.rows.Close()
	if !r.rows.Next() {
		if err := r.rows.Err(); err != nil {
			return wrapErr("QueryRow.Scan", err)
		}
		return wrapErr("QueryRow.Scan", sql.ErrNoRows)
	}
	err := r.rows.Scan(dest...)
	if err != nil {
		return wrapErr("QueryRow.Scan", err)
	}
	return wrapErr("QueryRow.Scan", r.rows.Close())
}

func wrapErr(prefix string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s: %w", callerOfCaller(2), prefix, err)
}

func callerOfCaller(depth int) string {
	caller := "unknown"
	pc := make([]uintptr, 3)
	const addedSkip = 3 // runtime.Callers, callerOfCaller, our caller (e.g. wrapErr or Rx)
	if n := runtime.Callers(addedSkip+depth-1, pc[:]); n > 0 {
		frames := runtime.CallersFrames(pc[:n])
		frame, _ := frames.Next()
		if frame.Function != "" {
			caller = frame.Function
		}
		// This is a special case.
		//
		// We expect people to wrap the Tx/Rx objects
		// in another domain-specific Tx/Rx object. That means
		// they almost certainly have matching Tx/Rx methods,
		// which aren't useful for debugging. So if we see that,
		// we remove it.
		if strings.HasSuffix(caller, ".Tx") || strings.HasSuffix(caller, ".Rx") {
			frame, more := frames.Next()
			if more && frame.Function != "" {
				caller = frame.Function
			}
		}
	}
	if i := strings.LastIndexByte(caller, '/'); i >= 0 {
		caller = caller[i+1:]
	}
	return caller
}
