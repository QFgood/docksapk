package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Name      string
	LAN       string
	Remote    string
	Demo      bool
	Webhooks  map[string]string
	Collector string
}

type Server struct {
	db           *sql.DB
	config       Config
	pairMu       sync.Mutex
	pairWindow   time.Time
	pairFailures int
}

type device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func New(db *sql.DB, config Config) http.Handler {
	s := &Server{db: db, config: config}
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/about", s.about)
	m.HandleFunc("POST /v1/pair", s.pair)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			fail(w, http.StatusServiceUnavailable, "database_unavailable")
			return
		}
		reply(w, 200, map[string]string{"status": "ok"})
	})
	m.HandleFunc("GET /v1/shortcuts", s.auth(s.shortcuts))
	m.HandleFunc("POST /v1/shortcuts", s.auth(s.createShortcut))
	m.HandleFunc("PATCH /v1/shortcuts/{id}", s.auth(s.modifyShortcut))
	m.HandleFunc("DELETE /v1/shortcuts/{id}", s.auth(s.modifyShortcut))
	m.HandleFunc("PUT /v1/shortcut-order", s.auth(s.orderShortcuts))
	m.HandleFunc("GET /v1/hosts/{id}/status", s.auth(s.host))
	m.HandleFunc("GET /v1/events", s.auth(s.events))
	m.HandleFunc("GET /v1/events/{id}", s.auth(s.event))
	m.HandleFunc("PUT /v1/events/{id}/read", s.auth(s.event))
	m.HandleFunc("GET /v1/notification-rules", s.auth(s.rules))
	m.HandleFunc("PUT /v1/notification-rules", s.auth(s.rules))
	m.HandleFunc("POST /v1/webhooks/{source}", s.webhook)
	m.HandleFunc("GET /v1/settings", s.auth(s.settings))
	m.HandleFunc("DELETE /v1/devices/current", s.auth(func(w http.ResponseWriter, r *http.Request, d device) {
		if _, err := Revoke(db, d.ID); err != nil {
			internal(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	if config.Demo {
		m.HandleFunc("GET /demo/", s.demo)
		m.HandleFunc("POST /demo/", s.demo)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		m.ServeHTTP(w, r)
	})
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "error", err)
	}
}

func fail(w http.ResponseWriter, status int, code string) {
	reply(w, status, map[string]string{"error": code})
}

func internal(w http.ResponseWriter, err error) {
	slog.Error("request failed", "error", err)
	fail(w, 500, "internal")
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			fail(w, 413, "body_too_large")
		} else {
			fail(w, 400, "invalid_json")
		}
		return false
	}
	if dec.Decode(new(any)) != io.EOF {
		fail(w, 400, "invalid_json")
		return false
	}
	return true
}

func (s *Server) instance() (string, error) {
	var id string
	err := s.db.QueryRow("SELECT id FROM instance WHERE singleton = 1").Scan(&id)
	return id, err
}

func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	id, err := s.instance()
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, map[string]any{
		"instance_id": id, "name": s.config.Name, "api_version": 1,
		"capabilities": []string{"shortcuts", "host", "events"},
	})
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code       string `json:"code"`
		Name       string `json:"device_name"`
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, err := s.instance()
	if err != nil {
		internal(w, err)
		return
	}
	if in.InstanceID != id || !cleanField(in.Name, 120) || len(strings.TrimSpace(in.Code)) != 12 {
		fail(w, 400, "invalid_pair_request")
		return
	}
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if time.Since(s.pairWindow) >= time.Minute {
		s.pairWindow = time.Now()
		s.pairFailures = 0
	}
	if s.pairFailures >= 10 {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "pair_rate_limited")
		return
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		internal(w, err)
		return
	}
	defer tx.Rollback()
	result, err := tx.Exec("DELETE FROM pairing_codes WHERE digest = ? AND expires > ?", digest(strings.ToUpper(strings.TrimSpace(in.Code))), time.Now().Unix())
	if err != nil {
		internal(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		internal(w, err)
		return
	}
	if count != 1 {
		s.pairFailures++
		fail(w, 401, "invalid_or_expired_code")
		return
	}
	d := device{ID: randomID(16), Name: in.Name}
	token := randomID(32)
	if _, err := tx.Exec("INSERT INTO devices VALUES (?, ?, ?, ?)", d.ID, d.Name, digest(token), now()); err != nil {
		internal(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		internal(w, err)
		return
	}
	reply(w, 201, map[string]any{"device": d, "token": token, "instance_id": id})
}

func (s *Server) auth(fn func(http.ResponseWriter, *http.Request, device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(token) != 64 {
			fail(w, 401, "invalid_device")
			return
		}
		var d device
		err := s.db.QueryRowContext(r.Context(), "SELECT id, name FROM devices WHERE digest = ?", digest(token)).Scan(&d.ID, &d.Name)
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, 401, "invalid_device")
			return
		}
		if err != nil {
			internal(w, err)
			return
		}
		fn(w, r, d)
	}
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request, d device) {
	reply(w, 200, map[string]any{
		"device": d, "lan_url": s.config.LAN, "tailnet_url": s.config.Remote,
		"push_status": "unconfigured",
	})
}

func version(n int64) string { return fmt.Sprintf("\"%d\"", n) }

func match(w http.ResponseWriter, r *http.Request, n int64) bool {
	w.Header().Set("ETag", version(n))
	if r.Header.Get("If-Match") == "" {
		fail(w, 428, "if_match_required")
		return false
	}
	if r.Header.Get("If-Match") != version(n) {
		fail(w, 412, "revision_conflict")
		return false
	}
	return true
}
