package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/QFgood/docksapk/backend/internal/collector"
)

func main() {
	socket := flag.String("socket", "/run/docks-collector/docker.sock", "read-only collector listener")
	docker := flag.String("docker-socket", "/var/run/docker.sock", "Docker daemon socket")
	flag.Parse()

	listener, err := collector.Listen(*socket)
	if err != nil {
		slog.Error("collector listen", "error", err)
		os.Exit(1)
	}
	defer listener.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: collector.Handler(*docker), ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 8 * time.Second, WriteTimeout: 8 * time.Second, IdleTimeout: 15 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("collector serve", "error", err)
		os.Exit(1)
	}
}
