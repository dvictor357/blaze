package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"unicode/utf8"

	_ "github.com/mattn/go-sqlite3"
)

type product struct {
	SKU       string `json:"sku"`
	Name      string `json:"name"`
	Total     int    `json:"total"`
	Available int    `json:"available"`
	Reserved  int    `json:"reserved"`
}

type reservation struct {
	ID        int64  `json:"id"`
	OrderID   string `json:"order_id"`
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
	Source    string `json:"source"`
	CreatedAt string `json:"created_at"`
}

type event struct {
	ID        int64  `json:"id"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Quantity  int    `json:"quantity"`
	OrderID   string `json:"order_id"`
	CreatedAt string `json:"created_at"`
}

type inventoryState struct {
	Product      product       `json:"product"`
	Reservations []reservation `json:"reservations"`
	Events       []event       `json:"events"`
}

type reserveInput struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	OrderID  string `json:"order_id"`
}

type reserveOutput struct {
	Reservation reservation `json:"reservation"`
	Product     product     `json:"product"`
	Idempotent  bool        `json:"idempotent"`
}

type domainError struct {
	Code    string
	Message string
}

func (e *domainError) Error() string { return e.Code + ": " + e.Message }

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() + "?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL&_txlock=immediate"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite has one writer. Serializing this small demo's connections avoids
	// lock-upgrade races; the conditional UPDATE still enforces the stock rule.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	const schema = `
CREATE TABLE IF NOT EXISTS products (
    sku TEXT PRIMARY KEY CHECK (sku = 'KB-001'),
    name TEXT NOT NULL,
    total INTEGER NOT NULL CHECK (total >= 0),
    available INTEGER NOT NULL CHECK (available >= 0 AND available <= total)
);
CREATE TABLE IF NOT EXISTS reservations (
    id INTEGER PRIMARY KEY,
    order_id TEXT NOT NULL UNIQUE CHECK (length(order_id) BETWEEN 1 AND 64),
    sku TEXT NOT NULL REFERENCES products(sku),
    quantity INTEGER NOT NULL CHECK (typeof(quantity) = 'integer' AND quantity BETWEEN 1 AND 100),
    source TEXT NOT NULL CHECK (source IN ('HTTP', 'MCP')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL CHECK (source IN ('HTTP', 'MCP')),
    status TEXT NOT NULL CHECK (status IN ('success', 'rejected')),
    code TEXT NOT NULL,
    message TEXT NOT NULL,
    quantity INTEGER NOT NULL,
    order_id TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
INSERT INTO products (sku, name, total, available)
VALUES ('KB-001', 'Mechanical Keyboard', 8, 8)
ON CONFLICT(sku) DO NOTHING;
`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize inventory: %w", err)
	}
	return &store{db: db}, nil
}

func readProduct(ctx context.Context, tx *sql.Tx) (product, error) {
	var p product
	err := tx.QueryRowContext(ctx, `SELECT sku, name, total, available, total - available FROM products WHERE sku = 'KB-001'`).Scan(&p.SKU, &p.Name, &p.Total, &p.Available, &p.Reserved)
	return p, err
}

func (s *store) state(ctx context.Context) (inventoryState, error) {
	state := inventoryState{Reservations: []reservation{}, Events: []event{}}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return state, err
	}
	defer tx.Rollback()
	state.Product, err = readProduct(ctx, tx)
	if err != nil {
		return state, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, order_id, sku, quantity, source, created_at FROM reservations ORDER BY id DESC`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var r reservation
		if err := rows.Scan(&r.ID, &r.OrderID, &r.SKU, &r.Quantity, &r.Source, &r.CreatedAt); err != nil {
			rows.Close()
			return state, err
		}
		state.Reservations = append(state.Reservations, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, source, status, code, message, quantity, order_id, created_at FROM events ORDER BY id DESC`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.ID, &e.Source, &e.Status, &e.Code, &e.Message, &e.Quantity, &e.OrderID, &e.CreatedAt); err != nil {
			rows.Close()
			return state, err
		}
		state.Events = append(state.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return state, err
	}
	return state, tx.Commit()
}

func (s *store) reserve(ctx context.Context, in reserveInput, source string) (reserveOutput, error) {
	var out reserveOutput
	if in.SKU != "KB-001" || in.Quantity < 1 || in.Quantity > 100 || utf8.RuneCountInString(in.OrderID) < 1 || utf8.RuneCountInString(in.OrderID) > 64 {
		return out, &domainError{"invalid_inventory_request", "SKU, quantity or order ID is outside inventory limits"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()

	var existing reservation
	err = tx.QueryRowContext(ctx, `SELECT id, order_id, sku, quantity, source, created_at FROM reservations WHERE order_id = ?`, in.OrderID).Scan(&existing.ID, &existing.OrderID, &existing.SKU, &existing.Quantity, &existing.Source, &existing.CreatedAt)
	code := "reserved"
	message := fmt.Sprintf("Reserved %d Mechanical Keyboard units for %s", in.Quantity, in.OrderID)
	switch {
	case err == nil:
		if existing.SKU != in.SKU || existing.Quantity != in.Quantity {
			return out, &domainError{"duplicate_order", "Order ID already has a different reservation"}
		}
		out.Reservation = existing
		out.Idempotent = true
		code = "already_reserved"
		message = "Existing reservation returned; stock was not changed"
	case !errors.Is(err, sql.ErrNoRows):
		return out, err
	default:
		result, err := tx.ExecContext(ctx, `UPDATE products SET available = available - ? WHERE sku = ? AND available >= ?`, in.Quantity, in.SKU, in.Quantity)
		if err != nil {
			return out, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return out, err
		}
		if changed != 1 {
			return out, &domainError{"insufficient_stock", "Not enough available stock; no inventory was changed"}
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO reservations (order_id, sku, quantity, source) VALUES (?, ?, ?, ?)`, in.OrderID, in.SKU, in.Quantity, source)
		if err != nil {
			return out, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return out, err
		}
		err = tx.QueryRowContext(ctx, `SELECT id, order_id, sku, quantity, source, created_at FROM reservations WHERE id = ?`, id).Scan(&out.Reservation.ID, &out.Reservation.OrderID, &out.Reservation.SKU, &out.Reservation.Quantity, &out.Reservation.Source, &out.Reservation.CreatedAt)
		if err != nil {
			return out, err
		}
	}
	out.Product, err = readProduct(ctx, tx)
	if err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO events (source, status, code, message, quantity, order_id) VALUES (?, 'success', ?, ?, ?, ?)`, source, code, message, in.Quantity, in.OrderID); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

func (s *store) recordRejection(ctx context.Context, source, code, message string, in reserveInput) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (source, status, code, message, quantity, order_id) VALUES (?, 'rejected', ?, ?, ?, ?)`, source, code, message, in.Quantity, in.OrderID)
	return err
}
