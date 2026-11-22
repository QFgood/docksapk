package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QFgood/docksapk/backend/internal/hub"
)

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:]); err != nil {
		slog.Error("docksd", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: docksd serve|pair-code|devices|revoke|shortcuts|import|seed-demo [flags]")
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	filename := flags.String("db", "./docks.db", "SQLite database path")
	var listen, cert, key, name, lan, remote, disk, socket, device, source, input, base *string
	var demo *bool
	switch command {
	case "serve":
		listen = flags.String("listen", "127.0.0.1:8080", "API listen address")
		cert = flags.String("tls-cert", "", "TLS chain PEM")
		key = flags.String("tls-key", "", "TLS key PEM")
		name = flags.String("name", "Ubuntu 服务", "instance display name")
		lan = flags.String("lan", "", "trusted LAN HTTPS API origin")
		remote = flags.String("remote", "", "trusted Tailscale HTTPS API origin")
		disk = flags.String("disk", "/", "filesystem path to monitor")
		socket = flags.String("collector", "", "optional read-only collector Unix socket")
		demo = flags.Bool("demo", false, "enable independent test project page")
	case "pair-code", "devices", "shortcuts":
	case "revoke":
		device = flags.String("device", "", "device ID")
	case "import":
		source = flags.String("source", "", "import source name")
		input = flags.String("file", "", "complete verified navigation export in JSON")
	case "seed-demo":
		base = flags.String("base", "", "HTTPS origin of the configured test project")
	default:
		return fmt.Errorf("unknown command %s", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	absolute, err := filepath.Abs(*filename)
	if err != nil {
		return err
	}
	db, err := hub.Open(absolute)
	if err != nil {
		return err
	}
	defer db.Close()

	switch command {
	case "serve":
		return serve(db, *listen, *cert, *key, *disk, hub.Config{
			Name: *name, LAN: *lan, Remote: *remote, Demo: *demo, Collector: *socket,
		})
	case "pair-code":
		code, err := hub.PairingCode(db)
		if err != nil {
			return err
		}
		fmt.Println(code)
	case "devices":
		rows, err := db.Query("SELECT id, name, created_at FROM devices ORDER BY created_at")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name, created string
			if err := rows.Scan(&id, &name, &created); err != nil {
				return err
			}
			fmt.Printf("%s\t%s\t%s\n", id, name, created)
		}
		return rows.Err()
	case "shortcuts":
		items, err := hub.ListShortcuts(context.Background(), db)
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Printf("%s\t%s\t%s\n", item.ID, item.Group, item.Name)
		}
	case "revoke":
		if *device == "" {
			return errors.New("--device is required")
		}
		removed, err := hub.Revoke(db, *device)
		if err != nil {
			return err
		}
		if !removed {
			return errors.New("device not found")
		}
	case "import":
		if !hub.ValidSource(*source) || *source == "manual" || *input == "" {
			return errors.New("--source and --file are required")
		}
		items, err := readExport(*input)
		if err == nil {
			err = hub.Import(context.Background(), db, *source, items)
		}
		if err != nil {
			cause := errors.New("导入失败，已保留上次成功的卡片。")
			return errors.Join(err, hub.ImportError(db, *source, cause))
		}
	case "seed-demo":
		if !hub.APIOrigin(*base) {
			return errors.New("--base must be an HTTPS origin")
		}
		return hub.Import(context.Background(), db, "sample", []hub.ImportItem{{
			SourceID: "demo-project", Card: hub.Card{Name: "测试项目", Group: "演示服务", Icon: "表",
				LAN: strings.TrimSuffix(*base, "/") + "/demo/"},
		}})
	}
	return nil
}

func readExport(filename string) ([]hub.ImportItem, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	const max = 4 << 20
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if len(data) > max {
		return nil, errors.New("navigation export too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var input struct {
		Complete bool             `json:"complete"`
		Items    []hub.ImportItem `json:"items"`
	}
	if err := decoder.Decode(&input); err != nil {
		return nil, err
	}
	if !input.Complete || input.Items == nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("a valid complete export is required")
	}
	return input.Items, nil
}

func serve(db *sql.DB, address, cert, key, disk string, config hub.Config) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	tls := cert != "" && key != ""
	if (cert == "") != (key == "") {
		return errors.New("configure both --tls-cert and --tls-key")
	}
	if ip := net.ParseIP(host); !tls && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("plain HTTP is limited to a loopback address behind a trusted HTTPS reverse proxy")
	}
	for _, address := range []string{config.LAN, config.Remote} {
		if address != "" && !hub.APIOrigin(address) {
			return errors.New("--lan and --remote must be HTTPS origins")
		}
	}
	if raw := os.Getenv("DOCKS_WEBHOOK_SECRETS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.Webhooks); err != nil {
			return errors.New("invalid DOCKS_WEBHOOK_SECRETS JSON")
		}
		for source, key := range config.Webhooks {
			if !hub.ValidSource(source) || len(key) < 24 || strings.TrimSpace(key) != key {
				return errors.New("invalid webhook source or key shorter than 24 characters")
			}
		}
	}
	if raw := os.Getenv("DOCKS_DOCKER_SHORTCUTS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &config.DockerShortcuts); err != nil || config.DockerShortcuts == nil {
			return errors.New("DOCKS_DOCKER_SHORTCUTS must be a JSON object")
		}
		for service, id := range config.DockerShortcuts {
			if strings.TrimSpace(service) != service || service == "" {
				return errors.New("DOCKS_DOCKER_SHORTCUTS contains an invalid service name")
			}
			if _, err := hex.DecodeString(id); err != nil || len(id) != 32 {
				return fmt.Errorf("invalid shortcut ID for Docker service %q", service)
			}
			config.DockerShortcuts[service] = strings.ToLower(id)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() { defer close(done); hub.WatchHost(ctx, db, disk, config.Collector) }()
	server := &http.Server{
		Addr: address, Handler: hub.New(db, config), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 45 * time.Second,
	}
	listenErr := make(chan error, 1)
	go func() {
		slog.Info("serving API", "listen", address, "tls", tls)
		if tls {
			listenErr <- server.ListenAndServeTLS(cert, key)
		} else {
			listenErr <- server.ListenAndServe()
		}
	}()
	select {
	case err = <-listenErr:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		err = server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
	}
	stop()
	<-done
	return err
}
