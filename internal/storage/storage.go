// Package storage provides SQLite-based request logging.
package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Storage wraps the SQLite database connection.
type Storage struct {
	db *sql.DB
}

// New creates a new storage instance with the database at dbPath.
func New(dbPath string) (*Storage, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil && filepath.Dir(dbPath) != "." {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &Storage{db: db}, nil
}

func runMigrations(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS request_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			model TEXT,
			headers TEXT,
			request_body TEXT,
			response_body TEXT,
			status_code INTEGER,
			elapsed_ms INTEGER,
			ttfb_ms INTEGER,
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			retry_count INTEGER DEFAULT 0,
			is_sse BOOLEAN DEFAULT FALSE,
			error_body TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_request_logs_created_at ON request_logs(created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_request_logs_status_code ON request_logs(status_code);
	`)
	if err != nil {
		return err
	}
	// Migration for existing databases
	db.Exec(`ALTER TABLE request_logs ADD COLUMN model TEXT`)
	db.Exec(`ALTER TABLE request_logs ADD COLUMN response_body TEXT`)
	db.Exec(`ALTER TABLE request_logs ADD COLUMN ttfb_ms INTEGER`)
	db.Exec(`ALTER TABLE request_logs ADD COLUMN input_tokens INTEGER DEFAULT 0`)
	db.Exec(`ALTER TABLE request_logs ADD COLUMN output_tokens INTEGER DEFAULT 0`)
	return nil
}

// Close closes the database connection.
func (s *Storage) Close() error {
	return s.db.Close()
}

// InsertLog inserts a new request log entry.
func (s *Storage) InsertLog(log *RequestLog) error {
	_, err := s.db.Exec(`
		INSERT INTO request_logs (method, path, model, headers, request_body, response_body, status_code, elapsed_ms, ttfb_ms, input_tokens, output_tokens, retry_count, is_sse, error_body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.Method, log.Path, log.Model, log.Headers, log.RequestBody, log.ResponseBody, log.StatusCode, log.ElapsedMs, log.TTFBMs, log.InputTokens, log.OutputTokens, log.RetryCount, log.IsSSE, log.ErrorBody)
	return err
}

// GetLogs returns paginated log entries.
func (s *Storage) GetLogs(limit, offset int) ([]RequestLog, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	rows, err := s.db.Query(`
		SELECT id, created_at, method, path, model, headers, request_body, response_body, status_code, elapsed_ms, ttfb_ms, input_tokens, output_tokens, retry_count, is_sse, error_body
		FROM request_logs
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []RequestLog
	for rows.Next() {
		var log RequestLog
		var createdAt sql.NullTime
		var model, headers, requestBody, responseBody, errorBody sql.NullString
		var statusCode sql.NullInt64
		var elapsedMs, ttfbMs sql.NullInt64
		var inputTokens, outputTokens sql.NullInt64
		var retryCount sql.NullInt64
		var isSSE sql.NullBool

		if err := rows.Scan(&log.ID, &createdAt, &log.Method, &log.Path, &model, &headers, &requestBody, &responseBody, &statusCode, &elapsedMs, &ttfbMs, &inputTokens, &outputTokens, &retryCount, &isSSE, &errorBody); err != nil {
			return nil, err
		}

		if createdAt.Valid {
			log.CreatedAt = createdAt.Time.Format(time.RFC3339)
		}
		log.Model = model.String
		log.Headers = headers.String
		log.RequestBody = requestBody.String
		log.ResponseBody = responseBody.String
		log.StatusCode = int(statusCode.Int64)
		log.ElapsedMs = elapsedMs.Int64
		log.TTFBMs = ttfbMs.Int64
		log.InputTokens = int(inputTokens.Int64)
		log.OutputTokens = int(outputTokens.Int64)
		log.RetryCount = int(retryCount.Int64)
		log.IsSSE = isSSE.Bool
		log.ErrorBody = errorBody.String

		logs = append(logs, log)
	}
	return logs, rows.Err()
}

// GetLogByID returns a single log entry by ID.
func (s *Storage) GetLogByID(id int64) (*RequestLog, error) {
	var log RequestLog
	var createdAt sql.NullTime
	var model, headers, requestBody, responseBody, errorBody sql.NullString
	var statusCode sql.NullInt64
	var elapsedMs, ttfbMs sql.NullInt64
	var inputTokens, outputTokens sql.NullInt64
	var retryCount sql.NullInt64
	var isSSE sql.NullBool

	err := s.db.QueryRow(`
		SELECT id, created_at, method, path, model, headers, request_body, response_body, status_code, elapsed_ms, ttfb_ms, input_tokens, output_tokens, retry_count, is_sse, error_body
		FROM request_logs
		WHERE id = ?
	`, id).Scan(&log.ID, &createdAt, &log.Method, &log.Path, &model, &headers, &requestBody, &responseBody, &statusCode, &elapsedMs, &ttfbMs, &inputTokens, &outputTokens, &retryCount, &isSSE, &errorBody)
	if err != nil {
		return nil, err
	}

	if createdAt.Valid {
		log.CreatedAt = createdAt.Time.Format(time.RFC3339)
	}
	log.Model = model.String
	log.Headers = headers.String
	log.RequestBody = requestBody.String
	log.ResponseBody = responseBody.String
	log.StatusCode = int(statusCode.Int64)
	log.ElapsedMs = elapsedMs.Int64
	log.TTFBMs = ttfbMs.Int64
	log.InputTokens = int(inputTokens.Int64)
	log.OutputTokens = int(outputTokens.Int64)
	log.RetryCount = int(retryCount.Int64)
	log.IsSSE = isSSE.Bool
	log.ErrorBody = errorBody.String

	return &log, nil
}

// DeleteOlderThan deletes logs older than the specified number of days.
func (s *Storage) DeleteOlderThan(days int) error {
	cutoff := time.Now().AddDate(0, 0, -days).Format(time.RFC3339)
	_, err := s.db.Exec(`DELETE FROM request_logs WHERE created_at < ?`, cutoff)
	return err
}
