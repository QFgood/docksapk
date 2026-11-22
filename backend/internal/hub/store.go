package hub

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

func Open(filename string) (*sql.DB, error) {
	dsn := (&url.URL{Scheme: "file", Path: filename}).String()
	// A single connection makes the migration's SQLite pragmas and transaction
	// ordering consistent for server requests and background collectors.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout = 5000"); err == nil {
		_, err = db.Exec(schema)
	}
	if err == nil {
		_, err = db.Exec("INSERT OR IGNORE INTO instance(singleton, id) VALUES (1, ?)", randomID(16))
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func now() string              { return stamp(time.Now()) }
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func randomID(bytes int) string {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func digest(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }

func pack(v any) string {
	result, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(result)
}

func PairingCode(db *sql.DB) (string, error) {
	code := strings.ToUpper(randomID(6))
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM pairing_codes WHERE expires <= ?", time.Now().Unix()); err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO pairing_codes VALUES (?, ?)", digest(code), time.Now().Add(5*time.Minute).Unix()); err != nil {
		return "", err
	}
	return code, tx.Commit()
}

func Revoke(db *sql.DB, id string) (bool, error) {
	result, err := db.Exec("DELETE FROM devices WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func httpsURL(value string) (*url.URL, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		strings.ContainsAny(value, "\\\r\n\t") || len(value) > 2048 {
		return nil, false
	}
	return u, true
}

func APIOrigin(value string) bool {
	u, ok := httpsURL(value)
	return ok && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func origin(value string) bool {
	return APIOrigin(value) && !strings.Contains(value, "#")
}

func cleanField(s string, max int) bool {
	return strings.TrimSpace(s) == s && len(s) > 0 && len(s) <= max && !strings.ContainsAny(s, "\r\n\t\x00")
}

func ValidSource(s string) bool {
	return cleanField(s, 80) && s == path.Base(s) && s != "." && s != ".." && !strings.ContainsAny(s, "\\?#")
}
