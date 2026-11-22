package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Card struct {
	Name     string   `json:"name"`
	Group    string   `json:"group_id"`
	Position int      `json:"position"`
	Icon     string   `json:"icon,omitempty"`
	LAN      string   `json:"lan_url"`
	Remote   string   `json:"tailnet_url,omitempty"`
	Origins  []string `json:"trusted_origins,omitempty"`
}

type Shortcut struct {
	Card
	ID         string `json:"id"`
	Host       string `json:"host_id"`
	Source     string `json:"source"`
	SourceID   string `json:"source_id,omitempty"`
	Revision   int64  `json:"revision"`
	Overridden bool   `json:"overridden"`
}

func (c Card) valid() bool {
	_, lan := httpsURL(c.LAN)
	_, remote := httpsURL(c.Remote)
	if !cleanField(c.Name, 180) || !cleanField(c.Group, 120) ||
		!lan || (c.Remote != "" && !remote) || c.Position < 0 || c.Position > 100000 ||
		len(c.Icon) > 80 || len(c.Origins) > 12 {
		return false
	}
	for _, value := range c.Origins {
		if !origin(value) {
			return false
		}
	}
	return true
}

func cards(tx *sql.Tx, rev int64, id string) ([]Shortcut, error) {
	rows, err := tx.Query(`
		SELECT id, COALESCE(source, 'manual'), COALESCE(source_id, ''), data, COALESCE(override, '')
		FROM shortcuts WHERE hidden = 0 AND (? = '' OR id = ?)`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Shortcut{}
	for rows.Next() {
		s := Shortcut{Host: "local", Revision: rev}
		var source, override string
		if err := rows.Scan(&s.ID, &s.Source, &s.SourceID, &source, &override); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(source), &s.Card); err != nil {
			return nil, err
		}
		if override != "" {
			s.Overridden = true
			// Overrides are field-based, including deliberately cleared values.
			if err := json.Unmarshal([]byte(override), &s.Card); err != nil {
				return nil, err
			}
		}
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		if a.Position != b.Position {
			return a.Position < b.Position
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return list, rows.Err()
}

func shortcutRevision(tx *sql.Tx) (int64, error) {
	var rev int64
	err := tx.QueryRow("SELECT shortcuts_version FROM instance WHERE singleton = 1").Scan(&rev)
	return rev, err
}

func updateShortcutRevision(tx *sql.Tx) error {
	_, err := tx.Exec("UPDATE instance SET shortcuts_version = shortcuts_version + 1 WHERE singleton = 1")
	return err
}

func (s *Server) shortcuts(w http.ResponseWriter, r *http.Request, d device) {
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	rev, err := shortcutRevision(tx)
	if err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("ETag", version(rev))
	if r.Header.Get("If-None-Match") == version(rev) {
		w.WriteHeader(304)
		return
	}
	items, err := cards(tx, rev, "")
	if err != nil {
		internal(w, err)
		return
	}
	type syncState struct {
		Source string `json:"source"`
		Last   string `json:"last_success"`
		Error  string `json:"error"`
	}
	syncs := []syncState{}
	rows, err := tx.Query("SELECT source, COALESCE(last_success,''), COALESCE(error,'') FROM import_state ORDER BY source")
	if err != nil {
		internal(w, err)
		return
	}
	for rows.Next() {
		var sync syncState
		if err = rows.Scan(&sync.Source, &sync.Last, &sync.Error); err != nil {
			break
		}
		syncs = append(syncs, sync)
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, map[string]any{"items": items, "revision": rev, "imports": syncs})
}

func (s *Server) createShortcut(w http.ResponseWriter, r *http.Request, d device) {
	var c Card
	if !decode(w, r, &c) {
		return
	}
	if !c.valid() {
		fail(w, 400, "invalid_shortcut")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key != "" && !cleanField(key, 120) {
		fail(w, 400, "invalid_key")
		return
	}
	fp := digest(pack(c))
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	if key != "" {
		if _, err := tx.Exec("DELETE FROM requests WHERE created_at < ?", time.Now().Add(-24*time.Hour).Unix()); err != nil {
			internal(w, err)
			return
		}
		var existing, response string
		err := tx.QueryRow("SELECT fingerprint, response FROM requests WHERE device_id = ? AND key = ?", d.ID, key).Scan(&existing, &response)
		if err == nil {
			if existing != fp {
				fail(w, 409, "idempotency_conflict")
				return
			}
			reply(w, 200, json.RawMessage(response))
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			internal(w, err)
			return
		}
	}
	id := randomID(16)
	if _, err := tx.Exec("INSERT INTO shortcuts(id, data) VALUES (?, ?)", id, pack(c)); err != nil {
		internal(w, err)
		return
	}
	if err := updateShortcutRevision(tx); err != nil {
		internal(w, err)
		return
	}
	rev, err := shortcutRevision(tx)
	if err != nil {
		internal(w, err)
		return
	}
	out := Shortcut{Card: c, ID: id, Host: "local", Source: "manual", Revision: rev}
	if key != "" {
		_, err = tx.Exec("INSERT INTO requests VALUES (?, ?, ?, ?, ?)", d.ID, key, fp, pack(out), time.Now().Unix())
		if err != nil {
			internal(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("ETag", version(rev))
	reply(w, 201, out)
}

type cardPatch struct {
	Name     *string   `json:"name,omitempty"`
	Group    *string   `json:"group_id,omitempty"`
	Position *int      `json:"position,omitempty"`
	Icon     *string   `json:"icon,omitempty"`
	LAN      *string   `json:"lan_url,omitempty"`
	Remote   *string   `json:"tailnet_url,omitempty"`
	Origins  *[]string `json:"trusted_origins,omitempty"`
	Clear    bool      `json:"clear_override,omitempty"`
}

func applyPatch(tx *sql.Tx, id string, patch cardPatch) (bool, error) {
	var source sql.NullString
	var data string
	var overrides sql.NullString
	err := tx.QueryRow("SELECT source, data, override FROM shortcuts WHERE id = ? AND hidden = 0", id).Scan(&source, &data, &overrides)
	if err != nil {
		return false, err
	}
	if patch.Clear {
		if !source.Valid {
			return false, nil
		}
		_, err = tx.Exec("UPDATE shortcuts SET override = NULL WHERE id = ?", id)
		return err == nil, err
	}
	var c Card
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return false, err
	}
	merged := map[string]json.RawMessage{}
	if overrides.Valid {
		if err := json.Unmarshal([]byte(overrides.String), &merged); err != nil {
			return false, err
		}
	}
	var updates map[string]json.RawMessage
	if err := json.Unmarshal([]byte(pack(patch)), &updates); err != nil {
		return false, err
	}
	for k, v := range updates {
		merged[k] = v
	}
	if err := json.Unmarshal([]byte(pack(merged)), &c); err != nil {
		return false, err
	}
	if !c.valid() {
		return false, nil
	}
	if source.Valid {
		_, err = tx.Exec("UPDATE shortcuts SET override = ? WHERE id = ?", pack(merged), id)
	} else {
		_, err = tx.Exec("UPDATE shortcuts SET data = ? WHERE id = ?", pack(c), id)
	}
	return err == nil, err
}

func (s *Server) modifyShortcut(w http.ResponseWriter, r *http.Request, d device) {
	var patch cardPatch
	if r.Method == "PATCH" && !decode(w, r, &patch) {
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	rev, err := shortcutRevision(tx)
	if err != nil {
		internal(w, err)
		return
	}
	if !match(w, r, rev) {
		return
	}
	id := r.PathValue("id")
	if r.Method == "DELETE" {
		var source sql.NullString
		err := tx.QueryRow("SELECT source FROM shortcuts WHERE id = ? AND hidden = 0", id).Scan(&source)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "shortcut_not_found")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		if source.Valid {
			_, err = tx.Exec("UPDATE shortcuts SET hidden = 2 WHERE id = ?", id)
		} else {
			_, err = tx.Exec("DELETE FROM shortcuts WHERE id = ?", id)
		}
		if err != nil {
			internal(w, err)
			return
		}
	} else {
		valid, err := applyPatch(tx, id, patch)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 404, "shortcut_not_found")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		if !valid {
			fail(w, 400, "invalid_shortcut")
			return
		}
	}
	if err := updateShortcutRevision(tx); err != nil {
		internal(w, err)
		return
	}
	var out []Shortcut
	if r.Method == "PATCH" {
		out, err = cards(tx, rev+1, id)
		if err != nil {
			internal(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("ETag", version(rev+1))
	if r.Method == "DELETE" {
		w.WriteHeader(204)
		return
	}
	reply(w, 200, out[0])
}

func (s *Server) orderShortcuts(w http.ResponseWriter, r *http.Request, d device) {
	var input struct {
		Group string   `json:"group_id"`
		IDs   []string `json:"ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !cleanField(input.Group, 120) || len(input.IDs) == 0 || len(input.IDs) > 1000 {
		fail(w, 400, "invalid_order")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	rev, err := shortcutRevision(tx)
	if err != nil {
		internal(w, err)
		return
	}
	if !match(w, r, rev) {
		return
	}
	items, err := cards(tx, rev, "")
	if err != nil {
		internal(w, err)
		return
	}
	group := map[string]bool{}
	for _, c := range items {
		if c.Group == input.Group {
			group[c.ID] = true
		}
	}
	if len(group) != len(input.IDs) {
		fail(w, 400, "invalid_order")
		return
	}
	for position, id := range input.IDs {
		if !group[id] {
			fail(w, 400, "invalid_order")
			return
		}
		delete(group, id)
		if _, err := applyPatch(tx, id, cardPatch{Position: &position}); err != nil {
			internal(w, err)
			return
		}
	}
	if err := updateShortcutRevision(tx); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	w.Header().Set("ETag", version(rev+1))
	w.WriteHeader(204)
}

type ImportItem struct {
	Card
	SourceID string `json:"source_id"`
}

func ImportError(db *sql.DB, source string, cause error) error {
	summary := strings.TrimSpace(cause.Error())
	if len(summary) > 240 {
		summary = summary[:240]
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO import_state(source, error) VALUES (?, ?)
		ON CONFLICT(source) DO UPDATE SET error = excluded.error`, source, summary)
	if err != nil {
		return err
	}
	if err := updateShortcutRevision(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func Import(ctx context.Context, db *sql.DB, source string, entries []ImportItem) error {
	if !ValidSource(source) || source == "manual" || entries == nil || len(entries) > 1000 {
		return errors.New("invalid import manifest")
	}
	seen := map[string]bool{}
	for _, c := range entries {
		if !c.valid() || !cleanField(c.SourceID, 180) || seen[c.SourceID] {
			return errors.New("invalid or duplicate import entry")
		}
		seen[c.SourceID] = true
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE shortcuts SET hidden = 1 WHERE source = ? AND hidden = 0", source); err != nil {
		return err
	}
	for _, c := range entries {
		_, err = tx.Exec(`INSERT INTO shortcuts(id, data, source, source_id) VALUES (?, ?, ?, ?)
			ON CONFLICT(source, source_id) DO UPDATE SET data = excluded.data,
			hidden = CASE WHEN shortcuts.hidden = 2 THEN 2 ELSE 0 END`, randomID(16), pack(c.Card), source, c.SourceID)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO import_state(source, last_success) VALUES (?, ?)
		ON CONFLICT(source) DO UPDATE SET last_success = excluded.last_success, error = NULL`, source, now())
	if err != nil {
		return err
	}
	if err := updateShortcutRevision(tx); err != nil {
		return err
	}
	return tx.Commit()
}
