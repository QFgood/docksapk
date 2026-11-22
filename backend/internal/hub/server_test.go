package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fixture struct {
	t      *testing.T
	path   string
	db     *sql.DB
	server *httptest.Server
	client *http.Client
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, path: filepath.Join(t.TempDir(), "docks.db")}
	f.start()
	t.Cleanup(func() { f.stop() })
	return f
}

func (f *fixture) start() {
	var err error
	f.db, err = Open(f.path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.server = httptest.NewTLSServer(New(f.db, Config{Name: "Test", Demo: true,
		Webhooks: map[string]string{"ci": "test-source-key-not-used-in-prod"}}))
	f.client = f.server.Client()
	f.client.Jar, _ = cookiejar.New(nil)
}

func (f *fixture) stop() { f.server.Close(); _ = f.db.Close() }

func (f *fixture) request(method, path, token string, body any, headers map[string]string) (int, http.Header, []byte) {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(pack(body))
	}
	r, err := http.NewRequest(method, f.server.URL+path, reader)
	if err != nil {
		f.t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return res.StatusCode, res.Header, data
}

func (f *fixture) expect(want int, method, path, token string, body any, headers map[string]string, out any) http.Header {
	f.t.Helper()
	code, h, data := f.request(method, path, token, body, headers)
	if code != want {
		f.t.Fatalf("%s %s: got %d want %d, %s", method, path, code, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			f.t.Fatal(err)
		}
	}
	return h
}

type paired struct {
	Device device
	Token  string
}

func (f *fixture) pair() paired {
	var about struct {
		ID string `json:"instance_id"`
	}
	f.expect(200, "GET", "/v1/about", "", nil, nil, &about)
	code, err := PairingCode(f.db)
	if err != nil {
		f.t.Fatal(err)
	}
	in := map[string]string{"code": code, "device_name": "Test phone", "instance_id": about.ID}
	var d paired
	f.expect(201, "POST", "/v1/pair", "", in, nil, &d)
	f.expect(401, "POST", "/v1/pair", "", in, nil, nil)
	if len(d.Token) != 64 {
		f.t.Fatal("missing device token")
	}
	return d
}

type cardList struct{ Items []Shortcut }

func TestPairAndShareCardThroughRestart(t *testing.T) {
	f := newFixture(t)
	first, second := f.pair(), f.pair()
	f.expect(401, "GET", "/v1/shortcuts", "", nil, nil, nil)
	var created, edited Shortcut
	card := Card{Name: "Original", Group: "Home", LAN: f.server.URL + "/demo/"}
	key := map[string]string{"Idempotency-Key": "request-1"}
	etag := f.expect(201, "POST", "/v1/shortcuts", first.Token, card, key, &created).Get("ETag")
	f.expect(200, "POST", "/v1/shortcuts", first.Token, card, key, nil)
	var list cardList
	f.expect(200, "GET", "/v1/shortcuts", second.Token, nil, nil, &list)
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("unexpected cards %+v", list.Items)
	}
	endpoint := "/v1/shortcuts/" + created.ID
	f.expect(428, "PATCH", endpoint, first.Token, map[string]string{"name": "Renamed"}, nil, nil)
	f.expect(200, "PATCH", endpoint, first.Token, map[string]string{"name": "Renamed"}, map[string]string{"If-Match": etag}, &edited)
	f.expect(412, "PATCH", endpoint, second.Token, map[string]string{"name": "Stale edit"}, map[string]string{"If-Match": etag}, nil)
	f.stop()
	f.start()
	h := f.expect(200, "GET", "/v1/shortcuts", second.Token, nil, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "Renamed" || edited.Name != "Renamed" {
		t.Fatalf("edit lost: %+v", list.Items)
	}
	f.expect(304, "GET", "/v1/shortcuts", second.Token, nil, map[string]string{"If-None-Match": h.Get("ETag")}, nil)
	f.expect(204, "DELETE", "/v1/devices/current", first.Token, nil, nil, nil)
	f.expect(401, "GET", "/v1/shortcuts", first.Token, nil, nil, nil)
	f.expect(200, "GET", "/v1/shortcuts", second.Token, nil, nil, nil)
}

func TestPairCodeIsAtomicallyConsumed(t *testing.T) {
	f := newFixture(t)
	var about struct {
		ID string `json:"instance_id"`
	}
	f.expect(200, "GET", "/v1/about", "", nil, nil, &about)
	code, err := PairingCode(f.db)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]string{"code": code, "device_name": "Concurrent phone", "instance_id": about.ID}
	codes := make(chan int, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			res, err := f.client.Post(f.server.URL+"/v1/pair", "application/json", strings.NewReader(pack(in)))
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			codes <- res.StatusCode
		}()
	}
	workers.Wait()
	close(codes)
	results := map[int]int{}
	for code := range codes {
		results[code]++
	}
	if results[201] != 1 || results[401] != 1 {
		t.Fatalf("pair results: %v", results)
	}
}

func TestImportPreservesOverridesAndIdentity(t *testing.T) {
	f := newFixture(t)
	d := f.pair()
	entry := ImportItem{SourceID: "stable-id", Card: Card{Name: "Original", Group: "Home", LAN: "https://example.net/old"}}
	importEntries := func(items []ImportItem) {
		t.Helper()
		if err := Import(context.Background(), f.db, "export", items); err != nil {
			t.Fatal(err)
		}
	}
	importEntries([]ImportItem{entry})
	var before, after cardList
	h := f.expect(200, "GET", "/v1/shortcuts", d.Token, nil, nil, &before)
	path := "/v1/shortcuts/" + before.Items[0].ID
	f.expect(200, "PATCH", path, d.Token, map[string]string{"name": "My override"}, map[string]string{"If-Match": h.Get("ETag")}, nil)
	entry.Name, entry.LAN = "New source name", "https://example.net/new"
	importEntries([]ImportItem{entry})
	importEntries([]ImportItem{entry})
	if err := Import(context.Background(), f.db, "export", []ImportItem{entry, entry}); err == nil {
		t.Fatal("duplicate export accepted")
	}
	h = f.expect(200, "GET", "/v1/shortcuts", d.Token, nil, nil, &after)
	if len(after.Items) != 1 || after.Items[0].ID != before.Items[0].ID ||
		after.Items[0].LAN != entry.LAN || after.Items[0].Name != "My override" {
		t.Fatalf("import changed identity or override: %+v", after.Items)
	}
	f.expect(200, "PATCH", path, d.Token, map[string]bool{"clear_override": true}, map[string]string{"If-Match": h.Get("ETag")}, nil)
	f.expect(200, "GET", "/v1/shortcuts", d.Token, nil, nil, &after)
	if after.Items[0].Name != entry.Name {
		t.Fatal("source name not restored")
	}
	importEntries([]ImportItem{})
	f.expect(200, "GET", "/v1/shortcuts", d.Token, nil, nil, &after)
	if len(after.Items) != 0 {
		t.Fatal("removed source still visible")
	}
}

func TestWebhookSharedReadAndDemoForm(t *testing.T) {
	f := newFixture(t)
	d1, d2 := f.pair(), f.pair()
	path := "/v1/webhooks/ci"
	key := "test-source-key-not-used-in-prod"
	body := map[string]string{"source_event_id": "event-1", "service": "Test", "level": "warning", "summary": "Test signal"}
	var first, duplicate Event
	f.expect(401, "POST", path, d1.Token, body, nil, nil)
	f.expect(200, "POST", path, key, body, nil, &first)
	f.expect(200, "POST", path, key, body, nil, &duplicate)
	if first.ID != duplicate.ID || first.ID == "" {
		t.Fatal("webhook duplicated")
	}
	f.expect(204, "PUT", "/v1/events/"+first.ID+"/read", d1.Token, nil, nil, nil)
	f.stop()
	f.start()
	var out Event
	f.expect(200, "GET", "/v1/events/"+first.ID, d2.Token, nil, nil, &out)
	if !out.Read || out.Summary != body["summary"] {
		t.Fatalf("read state lost: %+v", out)
	}
	f.expect(200, "GET", "/demo/", "", nil, nil, nil)
	res, err := f.client.PostForm(f.server.URL+"/demo/", url.Values{"message": {"<test> OK"}})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	html, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.Contains(string(html), "&lt;test&gt; OK") || !strings.Contains(string(html), "Cookie 已保留") {
		t.Fatalf("project form failed: %d %s", res.StatusCode, html)
	}
}

func TestShortcutOrderRejectsStaleAndInvalidRequestsAtomically(t *testing.T) {
	f := newFixture(t)
	first, second := f.pair(), f.pair()
	create := func(name, group string, position int) Shortcut {
		t.Helper()
		var item Shortcut
		f.expect(201, "POST", "/v1/shortcuts", first.Token,
			Card{Name: name, Group: group, Position: position, LAN: f.server.URL + "/demo/"}, nil, &item)
		return item
	}
	a := create("A", "Home", 0)
	b := create("B", "Home", 1)
	other := create("C", "Other", 7)

	// Invalid IDs are rejected even after earlier updates in the transaction.
	version := f.expect(200, "GET", "/v1/shortcuts", first.Token, nil, nil, nil).Get("ETag")
	guard := map[string]string{"If-Match": version}
	f.expect(400, "PUT", "/v1/shortcut-order", first.Token,
		map[string]any{"group_id": "Home", "ids": []string{b.ID, other.ID}}, guard, nil)
	f.expect(304, "GET", "/v1/shortcuts", second.Token, nil, map[string]string{"If-None-Match": version}, nil)
	var unchanged cardList
	f.expect(200, "GET", "/v1/shortcuts", second.Token, nil, nil, &unchanged)
	if len(unchanged.Items) != 3 || unchanged.Items[0].ID != a.ID || unchanged.Items[1].Position != 1 {
		t.Fatalf("invalid order changed the list: %+v", unchanged.Items)
	}

	reversed := map[string]any{"group_id": "Home", "ids": []string{b.ID, a.ID}}
	f.expect(204, "PUT", "/v1/shortcut-order", first.Token, reversed, guard, nil)
	f.expect(412, "PUT", "/v1/shortcut-order", second.Token,
		map[string]any{"group_id": "Home", "ids": []string{a.ID, b.ID}}, guard, nil)

	f.stop()
	f.start()
	var saved cardList
	f.expect(200, "GET", "/v1/shortcuts", second.Token, nil, nil, &saved)
	if len(saved.Items) != 3 || saved.Items[0].ID != b.ID || saved.Items[0].Position != 0 ||
		saved.Items[1].ID != a.ID || saved.Items[1].Position != 1 ||
		saved.Items[2].ID != other.ID || saved.Items[2].Position != 7 {
		t.Fatalf("unexpected order after restart: %+v", saved.Items)
	}
}
