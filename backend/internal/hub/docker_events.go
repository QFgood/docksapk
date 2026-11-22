package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/QFgood/docksapk/backend/internal/collector"
)

func pollDocker(ctx context.Context, db *sql.DB, client *http.Client, at time.Time, wasDown bool) error {
	until := at.Add(-3 * time.Second)
	var cursor string
	err := db.QueryRow("SELECT docker_until FROM collector_cursor WHERE singleton = 1").Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec("INSERT OR IGNORE INTO collector_cursor VALUES (1, ?)", stamp(until))
		return err
	}
	if err != nil {
		return err
	}
	since, err := time.Parse(time.RFC3339Nano, cursor)
	if err != nil {
		return err
	}
	if !until.After(since) {
		return nil
	}
	capped := until.Sub(since) > 6*time.Hour
	if capped {
		since = until.Add(-6 * time.Hour)
	}
	var batch collector.Batch
	if err := collector.Read(ctx, client, "/events?since="+stamp(since)+"&until="+stamp(until), &batch); err != nil {
		return err
	}
	return commitDockerBatch(ctx, db, batch, cursor, since, until, capped || wasDown)
}

func commitDockerBatch(ctx context.Context, db *sql.DB, batch collector.Batch, cursor string, since, until time.Time, lostHistory bool) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec("UPDATE collector_cursor SET docker_until = ? WHERE singleton = 1 AND docker_until = ?", stamp(until), cursor)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return err
	}

	if batch.Gap || lostHistory {
		_, err := recordEvent(tx, Event{Source: "docker", SourceID: "gap-" + stamp(since), Occurred: stamp(until),
			Service: "Docker", Level: "warning", Summary: "Docker 采集曾中断或超过回放范围，此时间段可能存在事件缺口。"})
		if err != nil {
			return err
		}
	}

	sort.SliceStable(batch.Items, func(i, j int) bool { return batch.Items[i].Time < batch.Items[j].Time })
	for _, entry := range batch.Items {
		occurred := time.Unix(0, entry.Time)
		if entry.ID == "" || entry.Time <= 0 || occurred.Before(since) || occurred.After(until) {
			continue
		}
		if err := recordDockerEvent(tx, entry, occurred); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func recordDockerEvent(tx *sql.Tx, entry collector.Event, occurred time.Time) error {
	event := Event{Source: "docker", SourceID: fmt.Sprintf("%s:%s:%d", entry.ID, entry.Action, entry.Time),
		Occurred: stamp(occurred), Service: entry.Service, Level: "info"}
	switch entry.Action {
	case "start":
		event.Summary = "容器启动"
	case "die":
		event.Summary = "容器退出"
		if entry.Exit != "" && entry.Exit != "0" {
			event.Summary += "，退出码 " + entry.Exit
			event.Level = "warning"
		}
	case "oom":
		event.Summary, event.Level = "容器内存不足（OOM）", "critical"
	case "health_status: unhealthy":
		event.Summary, event.Level = "容器健康检查失败", "warning"
	case "health_status: healthy":
		event.Summary = "容器健康检查通过"
	default:
		return nil
	}

	switch entry.Action {
	case "health_status: unhealthy", "health_status: healthy":
		// Bound the lookup to the container, even when Compose services share a name.
		// A replayed health event finds itself here and is ignored.
		healthPrefix := entry.ID + ":health_status:"
		previous, err := readEvent(tx.QueryRow("SELECT "+eventFields+` FROM events
			WHERE source = 'docker' AND source_id >= ? AND source_id < ? AND occurred_at <= ?
			ORDER BY occurred_at DESC, id DESC LIMIT 1`, healthPrefix, entry.ID+":health_status;", event.Occurred))
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case previous.Level == event.Level:
			return nil
		case event.Level == "info" && previous.Service == event.Service:
			event.Recovery = previous.ID
		}
	case "oom", "die":
		if event.Level != "info" {
			prefix := entry.ID + ":" + entry.Action
			var duplicate bool
			err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM events
				WHERE source = 'docker' AND source_id >= ? AND source_id < ?
				AND level = ? AND summary = ? AND occurred_at BETWEEN ? AND ?)`,
				prefix+":", prefix+";", event.Level, event.Summary, stamp(occurred.Add(-5*time.Minute)), event.Occurred).Scan(&duplicate)
			if err != nil || duplicate {
				return err
			}
		}
	}

	_, err := recordEvent(tx, event)
	return err
}
