package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/QFgood/docksapk/backend/internal/collector"
)

type Sample struct {
	At         string                `json:"sampled_at"`
	CPU        float64               `json:"cpu_percent"`
	MemUsed    uint64                `json:"memory_used"`
	MemTotal   uint64                `json:"memory_total"`
	DiskUsed   uint64                `json:"disk_used"`
	DiskTotal  uint64                `json:"disk_total"`
	RX         uint64                `json:"rx_bps"`
	TX         uint64                `json:"tx_bps"`
	Uptime     uint64                `json:"uptime_seconds"`
	Docker     string                `json:"docker_state"`
	DockerAt   string                `json:"docker_sampled_at,omitempty"`
	Containers []collector.Container `json:"containers"`
}

type counters struct {
	at                  time.Time
	total, idle, rx, tx uint64
}

func number(value string) uint64 {
	n, _ := strconv.ParseUint(value, 10, 64)
	return n
}

func delta(current, previous uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

func sample(disk string, last counters) (Sample, counters, error) {
	current := counters{at: time.Now()}
	result := Sample{At: stamp(current.at), Docker: "disabled", Containers: []collector.Container{}}
	cpu, err := os.ReadFile("/proc/stat")
	if err != nil {
		return result, last, err
	}
	fields := strings.Fields(strings.SplitN(string(cpu), "\n", 2)[0])
	if len(fields) < 9 || fields[0] != "cpu" {
		return result, last, errors.New("invalid CPU counters")
	}
	for i := 1; i <= 8; i++ {
		current.total += number(fields[i])
	}
	current.idle = number(fields[4]) + number(fields[5])
	memory, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return result, last, err
	}
	var available uint64
	for _, line := range strings.Split(string(memory), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			result.MemTotal = number(f[1]) * 1024
		case "MemAvailable:":
			available = number(f[1]) * 1024
		}
	}
	result.MemUsed = delta(result.MemTotal, available)
	var fs syscall.Statfs_t
	if err := syscall.Statfs(disk, &fs); err != nil {
		return result, last, err
	}
	result.DiskTotal = fs.Blocks * uint64(fs.Bsize)
	result.DiskUsed = delta(fs.Blocks, fs.Bfree) * uint64(fs.Bsize)
	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return result, last, err
	}
	result.Uptime = number(strings.SplitN(string(uptime), ".", 2)[0])
	netdev, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return result, last, err
	}
	for _, line := range strings.Split(string(netdev), "\n") {
		name, data, ok := strings.Cut(line, ":")
		f := strings.Fields(data)
		if !ok || strings.TrimSpace(name) == "lo" || len(f) < 16 {
			continue
		}
		current.rx += number(f[0])
		current.tx += number(f[8])
	}
	if !last.at.IsZero() {
		elapsed := current.at.Sub(last.at).Seconds()
		result.RX = uint64(float64(delta(current.rx, last.rx)) / elapsed)
		result.TX = uint64(float64(delta(current.tx, last.tx)) / elapsed)
		total, idle := delta(current.total, last.total), delta(current.idle, last.idle)
		if total > 0 {
			result.CPU = float64(delta(total, idle)) * 100 / float64(total)
		}
	}
	return result, current, nil
}

func updateSamples(db *sql.DB, s Sample, t time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT OR REPLACE INTO host_samples VALUES (?, ?)", t.Unix(), pack(s)); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO host_rollups VALUES (?, 1, ?, ?, ?, ?, ?)
	   ON CONFLICT(bucket) DO UPDATE SET samples = samples+1, cpu_sum = cpu_sum+excluded.cpu_sum,
	   memory_sum = memory_sum+excluded.memory_sum, disk_sum = disk_sum+excluded.disk_sum,
	   rx_sum = rx_sum+excluded.rx_sum, tx_sum = tx_sum+excluded.tx_sum`, t.Truncate(5*time.Minute).Unix(),
		s.CPU, s.MemUsed, s.DiskUsed, s.RX, s.TX)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM host_samples WHERE sampled_at < ?", t.Add(-24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM host_rollups WHERE bucket < ?", t.Add(-7*24*time.Hour).Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM events WHERE occurred_at < ?", stamp(t.Add(-30*24*time.Hour))); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) host(w http.ResponseWriter, r *http.Request, d device) {
	if r.PathValue("id") != "local" {
		fail(w, 404, "host_not_found")
		return
	}
	var data string
	err := s.db.QueryRowContext(r.Context(), "SELECT data FROM host_samples ORDER BY sampled_at DESC LIMIT 1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		reply(w, 200, map[string]string{"state": "unavailable"})
		return
	}
	if err != nil {
		internal(w, err)
		return
	}
	reply(w, 200, json.RawMessage(data))
}

func WatchHost(ctx context.Context, db *sql.DB, disk, socket string) {
	var last counters
	var containers []collector.Container
	var dockerAt string
	var client *http.Client
	if socket != "" {
		client = collector.UnixClient(socket, 8*time.Second)
	}
	var dockerState = "disabled"
	var dockerDown bool
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()

	for {
		s, current, err := sample(disk, last)
		if err == nil {
			last = current
			if client != nil {
				var latest []collector.Container
				if err := collector.Read(ctx, client, "/containers", &latest); err == nil {
					containers, dockerAt, dockerState = latest, now(), "ok"
				} else {
					dockerState = "unavailable"
				}
				if err := pollDocker(ctx, db, client, current.at, dockerDown); err != nil && ctx.Err() == nil {
					slog.Warn("Docker event collection unavailable", "error", err)
					dockerDown = true
				} else {
					dockerDown = false
				}
				s.Docker, s.DockerAt = dockerState, dockerAt
				if containers != nil {
					s.Containers = containers
				}
			}
			err = updateSamples(db, s, current.at)
			if err == nil {
				err = diskEvent(db, s)
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("host sample unavailable", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func diskEvent(db *sql.DB, s Sample) error {
	if s.DiskTotal == 0 {
		return nil
	}
	ratio := float64(s.DiskUsed) / float64(s.DiskTotal)
	var id, level string
	err := db.QueryRow("SELECT id, level FROM events WHERE source = 'host:disk' ORDER BY occurred_at DESC LIMIT 1").Scan(&id, &level)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	e := Event{Source: "host:disk", SourceID: s.At, Occurred: s.At, Service: "Ubuntu"}
	switch {
	case ratio >= .90 && level != "warning":
		e.Level, e.Summary = "warning", fmt.Sprintf("磁盘使用率达到 %.0f%%", ratio*100)
	case ratio <= .85 && level == "warning":
		e.Level, e.Summary, e.Recovery = "info", "磁盘使用率已恢复", id
	default:
		return nil
	}
	_, err = RecordEvent(db, e)
	return err
}

func pollDocker(ctx context.Context, db *sql.DB, client *http.Client, at time.Time, wasDown bool) error {
	until := at.Add(-3 * time.Second)
	var cursor string
	err := db.QueryRow("SELECT docker_until FROM collector_cursor WHERE singleton = 1").Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec("INSERT INTO collector_cursor VALUES (1, ?)", stamp(until))
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
	if batch.Gap || capped || wasDown {
		_, err := RecordEvent(db, Event{Source: "docker", SourceID: "gap-" + stamp(since), Occurred: stamp(until),
			Service: "Docker", Level: "warning", Summary: "Docker 采集曾中断或超过回放范围，此时间段可能存在事件缺口。"})
		if err != nil {
			return err
		}
	}
	for _, entry := range batch.Items {
		e := Event{Source: "docker", SourceID: fmt.Sprintf("%s:%s:%d", entry.ID, entry.Action, entry.Time),
			Occurred: stamp(time.Unix(0, entry.Time)), Service: entry.Service, Level: "info"}
		switch entry.Action {
		case "start":
			e.Summary = "容器启动"
		case "die":
			e.Summary = "容器退出"
			if entry.Exit != "" && entry.Exit != "0" {
				e.Summary += "，退出码 " + entry.Exit
				e.Level = "warning"
			}
		case "oom":
			e.Summary, e.Level = "容器内存不足（OOM）", "critical"
		case "health_status: unhealthy":
			e.Summary, e.Level = "容器健康检查失败", "warning"
		case "health_status: healthy":
			e.Summary = "容器健康检查通过"
			_ = db.QueryRow(`SELECT id FROM events WHERE source = 'docker' AND source_id LIKE ?
			    AND occurred_at <= ? AND summary IN ('容器健康检查通过', '容器健康检查失败')
			    ORDER BY occurred_at DESC LIMIT 1`, entry.ID+":health_status: unhealthy:%", e.Occurred).Scan(&e.Recovery)
		}
		if _, err := RecordEvent(db, e); err != nil {
			return err
		}
	}
	_, err = db.Exec("UPDATE collector_cursor SET docker_until = ? WHERE singleton = 1", stamp(until))
	return err
}
