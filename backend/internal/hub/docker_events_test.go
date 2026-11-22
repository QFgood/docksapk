package hub

import (
	"context"
	"testing"
	"time"

	"github.com/QFgood/docksapk/backend/internal/collector"
)

func TestDockerAlertsRecoverOnceAcrossBatchesAndRestart(t *testing.T) {
	f := newFixture(t)
	phone := f.pair()
	t0 := time.Now().Add(-time.Hour).UTC()
	cursor := stamp(t0)
	if _, err := f.db.Exec("INSERT INTO collector_cursor VALUES (1, ?)", cursor); err != nil {
		t.Fatal(err)
	}

	event := func(id, action string, offset time.Duration, exit string) collector.Event {
		return collector.Event{ID: id, Action: action, Service: "shared-project",
			Time: t0.Add(offset).UnixNano(), Exit: exit}
	}
	apply := func(old string, from, to time.Duration, items ...collector.Event) {
		t.Helper()
		if err := commitDockerBatch(context.Background(), f.db, collector.Batch{Items: items}, old,
			t0.Add(from), t0.Add(to), false); err != nil {
			t.Fatal(err)
		}
	}

	// Docker may return the batch out of order, including another container
	// in the same Compose project.
	warning := event("aa", "health_status: unhealthy", 10*time.Second, "")
	otherWarning := event("bb", "health_status: unhealthy", 20*time.Second, "")
	apply(cursor, 0, time.Minute,
		event("aa", "health_status: unhealthy", 30*time.Second, ""), otherWarning, warning,
		event("aa", "die", 50*time.Second, "1"), event("aa", "die", 55*time.Second, "1"))
	f.stop()
	f.start()

	old := stamp(t0.Add(time.Minute))
	recovery := event("aa", "health_status: healthy", 90*time.Second, "")
	apply(old, time.Minute, 2*time.Minute, recovery,
		event("aa", "health_status: healthy", 95*time.Second, ""),
		event("aa", "oom", 100*time.Second, ""),
		event("aa", "oom", 102*time.Second, ""))
	// An overlapping worker cannot move an already advanced cursor.
	apply(old, time.Minute, 3*time.Minute, event("aa", "start", 150*time.Second, ""))
	apply(stamp(t0.Add(2*time.Minute)), 2*time.Minute, 8*time.Minute,
		event("aa", "health_status: unhealthy", 3*time.Minute, ""),
		event("aa", "die", 7*time.Minute, "1"))

	var history struct{ Items []Event }
	f.expect(200, "GET", "/v1/events", phone.Token, nil, nil, &history)
	if len(history.Items) != 7 {
		t.Fatalf("expected seven retained events, got %+v", history.Items)
	}
	var first, recovered Event
	for _, e := range history.Items {
		if e.Occurred == stamp(time.Unix(0, warning.Time)) {
			first = e
		}
		if e.Occurred == stamp(time.Unix(0, recovery.Time)) {
			recovered = e
		}
	}
	if first.ID == "" || recovered.Recovery != first.ID {
		t.Fatalf("unexpected recovery link: first=%+v recovery=%+v", first, recovered)
	}
}

func TestDockerBatchRollsBackCursorAndHistoryTogether(t *testing.T) {
	f := newFixture(t)
	t0 := time.Now().Add(-time.Hour).UTC()
	cursor := stamp(t0)
	if _, err := f.db.Exec("INSERT INTO collector_cursor VALUES (1, ?)", cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`CREATE TRIGGER fail_alarm BEFORE INSERT ON events
		WHEN NEW.level = 'critical' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}

	batch := collector.Batch{Gap: true, Items: []collector.Event{
		{ID: "aa", Action: "start", Service: "demo", Time: t0.Add(time.Second).UnixNano()},
		{ID: "aa", Action: "oom", Service: "demo", Time: t0.Add(2 * time.Second).UnixNano()},
	}}
	commit := func() error {
		return commitDockerBatch(context.Background(), f.db, batch, cursor, t0, t0.Add(time.Minute), false)
	}
	if err := commit(); err == nil {
		t.Fatal("fixture failure did not abort the batch")
	}
	var after string
	var count int
	if err := f.db.QueryRow("SELECT docker_until FROM collector_cursor").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if after != cursor || count != 0 {
		t.Fatalf("partial batch persisted: cursor=%s events=%d", after, count)
	}
	if _, err := f.db.Exec("DROP TRIGGER fail_alarm"); err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected resumed batch to contain gap, start and OOM, got %d events", count)
	}
}
