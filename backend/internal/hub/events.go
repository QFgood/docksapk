package hub

import (
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id"`
	Occurred   string `json:"occurred_at"`
	Received   string `json:"received_at"`
	Service    string `json:"service"`
	Level      string `json:"level"`
	Summary    string `json:"summary"`
	ShortcutID string `json:"shortcut_id,omitempty"`
	Recovery   string `json:"recovery_of,omitempty"`
	Read       bool   `json:"read"`
}

const eventFieldsBeforeShortcut = "id, source, source_id, occurred_at, received_at, service, level, summary, "
const eventFieldsAfterShortcut = ", recovery_of, read"
const eventFields = eventFieldsBeforeShortcut + "shortcut_id" + eventFieldsAfterShortcut

// Resolve the configured Docker link when reading history, so changing an
// association or hiding a card also updates previously collected events.
const apiEventFields = eventFieldsBeforeShortcut + `CASE
	WHEN source = 'docker' AND shortcut_id = '' AND substr(source_id, 1, 4) <> 'gap-' THEN COALESCE(
		(SELECT s.id FROM json_each(?) AS mapping
		 JOIN shortcuts AS s ON s.id = mapping.value AND s.hidden = 0
		 WHERE mapping.key = events.service), '')
	ELSE shortcut_id END` + eventFieldsAfterShortcut

func readEvent(row interface{ Scan(...any) error }) (Event, error) {
	var e Event
	err := row.Scan(&e.ID, &e.Source, &e.SourceID, &e.Occurred, &e.Received, &e.Service, &e.Level, &e.Summary, &e.ShortcutID, &e.Recovery, &e.Read)
	return e, err
}

func validLevel(level string) bool {
	return level == "info" || level == "warning" || level == "critical"
}

func RecordEvent(db *sql.DB, e Event) (Event, error) {
	tx, err := db.Begin()
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()
	out, err := recordEvent(tx, e)
	if err != nil {
		return Event{}, err
	}
	return out, tx.Commit()
}

func recordEvent(tx *sql.Tx, e Event) (Event, error) {
	if e.Occurred == "" {
		e.Occurred = now()
	} else {
		t, parseErr := time.Parse(time.RFC3339Nano, e.Occurred)
		if parseErr != nil {
			return Event{}, parseErr
		}
		e.Occurred = stamp(t)
	}
	e.ID, e.Received = randomID(16), now()
	if e.Recovery != "" {
		var id string
		err := tx.QueryRow("SELECT id FROM events WHERE id = ? AND source = ? AND service = ?", e.Recovery, e.Source, e.Service).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Event{}, err
		}
		e.Recovery = id
	}
	_, err := tx.Exec(`INSERT INTO events(id, source, source_id, occurred_at, received_at, service, level, summary, shortcut_id, recovery_of)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(source, source_id) DO NOTHING`,
		e.ID, e.Source, e.SourceID, e.Occurred, e.Received, e.Service, e.Level, e.Summary, e.ShortcutID, e.Recovery)
	if err != nil {
		return Event{}, err
	}
	return readEvent(tx.QueryRow("SELECT "+eventFields+" FROM events WHERE source = ? AND source_id = ?", e.Source, e.SourceID))
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	source := r.PathValue("source")
	secret := s.config.Webhooks[source]
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if secret == "" || auth == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(digest(auth)), []byte(digest(secret))) != 1 {
		fail(w, 401, "invalid_webhook_key")
		return
	}
	var input struct {
		SourceID   string `json:"source_event_id"`
		Occurred   string `json:"occurred_at"`
		Service    string `json:"service"`
		Level      string `json:"level"`
		Summary    string `json:"summary"`
		ShortcutID string `json:"shortcut_id"`
		Recovery   string `json:"recovery_of"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !cleanField(input.SourceID, 180) || !cleanField(input.Service, 180) ||
		!validLevel(input.Level) || !cleanField(input.Summary, 1000) || len(input.ShortcutID) > 32 || len(input.Recovery) > 32 {
		fail(w, 400, "invalid_event")
		return
	}
	if input.Occurred != "" {
		t, err := time.Parse(time.RFC3339Nano, input.Occurred)
		if err != nil || t.After(time.Now().Add(5*time.Minute)) || t.Before(time.Now().Add(-30*24*time.Hour)) {
			fail(w, 400, "invalid_event_time")
			return
		}
	}
	e, err := RecordEvent(s.db, Event{Source: "webhook:" + source, SourceID: input.SourceID, Occurred: input.Occurred,
		Service: input.Service, Level: input.Level, Summary: input.Summary, ShortcutID: input.ShortcutID, Recovery: input.Recovery})
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, e)
}

func (s *Server) event(w http.ResponseWriter, r *http.Request, d device) {
	id := r.PathValue("id")
	if r.Method == "PUT" {
		res, err := s.db.ExecContext(r.Context(), "UPDATE events SET read = 1 WHERE id = ?", id)
		if err != nil {
			internal(w, err)
			return
		}
		n, err := res.RowsAffected()
		if err != nil {
			internal(w, err)
			return
		}
		if n == 0 {
			fail(w, 404, "event_not_found")
			return
		}
		w.WriteHeader(204)
		return
	}
	e, err := readEvent(s.db.QueryRowContext(r.Context(), "SELECT "+apiEventFields+" FROM events WHERE id = ?", s.dockerShortcuts, id))
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 404, "event_not_found")
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, e)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, d device) {
	q := r.URL.Query()
	limit := 30
	var err error
	if value := q.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			fail(w, 400, "invalid_limit")
			return
		}
	}
	level, service := q.Get("level"), q.Get("service")
	if (level != "" && !validLevel(level)) || len(service) > 180 {
		fail(w, 400, "invalid_filter")
		return
	}
	var at, id string
	if cursor := q.Get("cursor"); cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		var ok bool
		at, id, ok = strings.Cut(string(raw), "|")
		if err != nil || !ok || len(id) != 32 || len(at) != len(now()) {
			fail(w, 400, "invalid_cursor")
			return
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var unread int
	if err := tx.QueryRow("SELECT count(*) FROM events WHERE read = 0").Scan(&unread); err != nil {
		internal(w, err)
		return
	}
	rows, err := tx.Query("SELECT "+apiEventFields+` FROM events WHERE (? = '' OR level = ?) AND (? = '' OR service = ?)
		AND (? = '' OR (occurred_at, id) < (?, ?)) ORDER BY occurred_at DESC, id DESC LIMIT ?`,
		s.dockerShortcuts, level, level, service, service, at, at, id, limit+1)
	if err != nil {
		internal(w, err)
		return
	}
	defer rows.Close()
	items := []Event{}
	for rows.Next() {
		e, err := readEvent(rows)
		if err != nil {
			internal(w, err)
			return
		}
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		internal(w, err)
		return
	}
	cursor := ""
	if len(items) > limit {
		e := items[limit-1]
		cursor = base64.RawURLEncoding.EncodeToString([]byte(e.Occurred + "|" + e.ID))
		items = items[:limit]
	}
	reply(w, 200, map[string]any{"items": items, "next_cursor": cursor, "unread": unread})
}

type notificationRules struct {
	Enabled         bool     `json:"enabled"`
	Levels          []string `json:"levels"`
	ShowServiceName bool     `json:"show_service_name"`
}

func (s *Server) rules(w http.ResponseWriter, r *http.Request, d device) {
	var input notificationRules
	if r.Method == "PUT" {
		if !decode(w, r, &input) {
			return
		}
		seen := map[string]bool{}
		for _, level := range input.Levels {
			if !validLevel(level) || seen[level] {
				fail(w, 400, "invalid_rules")
				return
			}
			seen[level] = true
		}
		if input.Levels == nil {
			input.Levels = []string{}
		}
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	var rev int64
	if err := tx.QueryRow("SELECT rules_version FROM instance WHERE singleton = 1").Scan(&rev); err != nil {
		internal(w, err)
		return
	}
	if r.Method == "GET" {
		data := pack(notificationRules{Enabled: true, Levels: []string{"warning", "critical"}})
		err := tx.QueryRow("SELECT data FROM notification_rules WHERE singleton = 1").Scan(&data)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			internal(w, err)
			return
		}
		w.Header().Set("ETag", version(rev))
		w.WriteHeader(200)
		_, _ = w.Write([]byte(data))
		return
	}
	if !match(w, r, rev) {
		return
	}
	if _, err := tx.Exec(`INSERT INTO notification_rules VALUES (1, ?) ON CONFLICT(singleton) DO UPDATE SET data = excluded.data`, pack(input)); err != nil {
		internal(w, err)
		return
	}
	if _, err := tx.Exec("UPDATE instance SET rules_version = rules_version + 1 WHERE singleton = 1"); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("ETag", version(rev+1))
	reply(w, 200, input)
}
