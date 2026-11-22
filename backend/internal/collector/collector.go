// Package collector exposes only fixed read operations on the Docker daemon.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Health  string `json:"health"`
	Project string `json:"project,omitempty"`
}

type Event struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Service string `json:"service"`
	Time    int64  `json:"time_nano"`
	Exit    string `json:"exit_code,omitempty"`
}

type Batch struct {
	Items []Event `json:"items"`
	Gap   bool    `json:"gap"`
}

func UnixClient(socket string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func request(ctx context.Context, c *http.Client, uri string) (io.ReadCloser, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local"+uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(r)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("collector peer returned %d", resp.StatusCode)
	}
	return resp.Body, nil
}

func Read(ctx context.Context, c *http.Client, uri string, out any) error {
	body, err := request(ctx, c, uri)
	if err != nil {
		return err
	}
	defer body.Close()
	return json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(out)
}

func Handler(dockerSocket string) http.Handler {
	client := UnixClient(dockerSocket, 6*time.Second)
	m := http.NewServeMux()
	m.HandleFunc("GET /containers", func(w http.ResponseWriter, r *http.Request) {
		var raw []struct {
			ID     string
			Names  []string
			State  string
			Status string
			Labels map[string]string
		}
		if err := Read(r.Context(), client, "/containers/json?all=1", &raw); err != nil {
			http.Error(w, "Docker snapshot unavailable", 503)
			return
		}
		items := make([]Container, 0, len(raw))
		for _, c := range raw {
			name := c.ID
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			health := ""
			if c.State == "running" {
				for _, status := range []string{"healthy", "unhealthy", "starting"} {
					if strings.HasSuffix(c.Status, "("+status+")") || strings.HasSuffix(c.Status, "(health: "+status+")") {
						health = status
					}
				}
			}
			items = append(items, Container{ID: c.ID, Name: name, State: c.State, Health: health, Project: c.Labels["com.docker.compose.project"]})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(items)
	})
	m.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		since, err1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("since"))
		until, err2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("until"))
		if err1 != nil || err2 != nil || until.Before(since) || until.After(time.Now().Add(-time.Second)) || until.Sub(since) > 6*time.Hour {
			http.Error(w, "Invalid event interval", 400)
			return
		}
		// Docker interprets numeric timestamps as seconds plus fractional seconds.
		dockerTime := func(t time.Time) string { return fmt.Sprintf("%d.%09d", t.Unix(), t.Nanosecond()) }
		params := url.Values{"since": {dockerTime(since)}, "until": {dockerTime(until)},
			"filters": {`{"type":["container"]}`}}
		body, err := request(r.Context(), client, "/events?"+params.Encode())
		if err != nil {
			http.Error(w, "Docker events unavailable", 503)
			return
		}
		defer body.Close()
		items := make([]Event, 0)
		dec := json.NewDecoder(io.LimitReader(body, 4<<20))
		count := 0
		for {
			var raw struct {
				Action   string
				TimeNano int64
				Actor    struct {
					ID         string
					Attributes map[string]string
				}
			}
			err := dec.Decode(&raw)
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "Docker events unreadable", 503)
				return
			}
			count++
			switch raw.Action {
			case "start", "die", "oom", "health_status: healthy", "health_status: unhealthy":
				service := raw.Actor.Attributes["com.docker.compose.project"]
				if service == "" {
					service = raw.Actor.Attributes["name"]
				}
				if service == "" {
					service = "Docker"
				}
				items = append(items, Event{ID: raw.Actor.ID, Action: raw.Action, Time: raw.TimeNano,
					Service: service, Exit: raw.Actor.Attributes["exitCode"]})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Batch{Items: items, Gap: count >= 256})
	})
	return m
}

func Listen(socket string) (net.Listener, error) {
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s is not a socket", socket)
		}
		conn, dialErr := net.DialTimeout("unix", socket, time.Second)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("collector already running on %s", socket)
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0660); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
