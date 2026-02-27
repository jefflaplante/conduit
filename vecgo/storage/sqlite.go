package storage

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	_ "modernc.org/sqlite"
)

// SQLite is a persistent storage implementation using SQLite.
type SQLite struct {
	db   *sql.DB
	path string
}

// NewSQLite creates a new SQLite storage at the given path.
func NewSQLite(path string) (*SQLite, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	s := &SQLite{db: db, path: path}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

func (s *SQLite) init() error {
	// Configure SQLite
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := s.db.Exec(p); err != nil {
			return fmt.Errorf("pragma failed: %w", err)
		}
	}

	// Create tables
	schema := `
		CREATE TABLE IF NOT EXISTS vectors (
			id TEXT PRIMARY KEY,
			embedding BLOB NOT NULL,
			metadata TEXT
		);
		CREATE TABLE IF NOT EXISTS hnsw_graph (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			data BLOB NOT NULL
		);
	`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("schema creation failed: %w", err)
	}

	return nil
}

// Save stores vectors in the database.
func (s *SQLite) Save(ctx context.Context, vectors []Vector) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		"INSERT OR REPLACE INTO vectors (id, embedding, metadata) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, v := range vectors {
		embBytes := encodeFloat32Slice(v.Embedding)
		var metaJSON []byte
		if v.Metadata != nil {
			metaJSON, _ = json.Marshal(v.Metadata)
		}
		if _, err := stmt.ExecContext(ctx, v.ID, embBytes, metaJSON); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Load returns all stored vectors.
func (s *SQLite) Load(ctx context.Context) ([]Vector, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, embedding, metadata FROM vectors")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vectors []Vector
	for rows.Next() {
		var v Vector
		var embBytes []byte
		var metaJSON sql.NullString

		if err := rows.Scan(&v.ID, &embBytes, &metaJSON); err != nil {
			return nil, err
		}
		v.Embedding = decodeFloat32Slice(embBytes)
		if metaJSON.Valid && metaJSON.String != "" {
			json.Unmarshal([]byte(metaJSON.String), &v.Metadata)
		}
		vectors = append(vectors, v)
	}

	return vectors, rows.Err()
}

// Delete removes vectors by ID.
func (s *SQLite) Delete(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, "DELETE FROM vectors WHERE id = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// SaveGraph stores the HNSW graph data.
func (s *SQLite) SaveGraph(ctx context.Context, data []byte) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT OR REPLACE INTO hnsw_graph (id, data) VALUES (1, ?)", data)
	return err
}

// LoadGraph returns the stored HNSW graph data.
func (s *SQLite) LoadGraph(ctx context.Context) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM hnsw_graph WHERE id = 1").Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return data, err
}

// Close closes the database connection.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// encodeFloat32Slice converts []float32 to []byte.
func encodeFloat32Slice(f []float32) []byte {
	buf := make([]byte, len(f)*4)
	for i, v := range f {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// decodeFloat32Slice converts []byte to []float32.
func decodeFloat32Slice(b []byte) []float32 {
	f := make([]float32, len(b)/4)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return f
}
