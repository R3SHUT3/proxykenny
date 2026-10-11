// pklab : web app de démonstration de proxykenny (galerie, comparaisons, bac à sable, bancs d'essai).
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"proxykenny/internal/lab"
)

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	health := flag.Bool("healthcheck", false, "vérifie que le lab local répond")
	flag.Parse()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if *health {
		c := http.Client{Timeout: 2 * time.Second}
		resp, err := c.Get("http://127.0.0.1" + env("LAB_BIND", ":8090") + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}

	key, err1 := hex.DecodeString(os.Getenv("KP_KEY"))
	salt, err2 := hex.DecodeString(os.Getenv("KP_SALT"))
	public := os.Getenv("LAB_PROXY_PUBLIC")
	if err1 != nil || err2 != nil || len(key) == 0 || public == "" {
		fmt.Fprintln(os.Stderr, "configuration : KP_KEY, KP_SALT (hex) et LAB_PROXY_PUBLIC sont requis")
		os.Exit(2)
	}
	maxUp, _ := strconv.Atoi(env("LAB_MAX_UPLOAD_MB", "30"))
	cfg := lab.Config{
		Bind:          env("LAB_BIND", ":8090"),
		MetricsBind:   env("LAB_METRICS_BIND", ":8091"),
		ProxyInternal: env("LAB_PROXY_INTERNAL", "http://proxykenny:8080"),
		ProxyPublic:   public,
		Key:           key,
		Salt:          salt,
		ImagesDir:     env("LAB_IMAGES_DIR", "/images"),
		User:          env("LAB_USER", "admin"),
		Password:      os.Getenv("LAB_PASSWORD"),
		GrafanaURL:    os.Getenv("LAB_GRAFANA_URL"),
		MaxUploadMB:   maxUp,
	}
	if cfg.Password == "" {
		slog.Warn("LAB_PASSWORD vide : le lab est accessible sans authentification")
	}
	srv, err := lab.NewServer(lab.New(cfg))
	if err != nil {
		slog.Error("initialisation", "err", err)
		os.Exit(1)
	}

	web := &http.Server{Addr: cfg.Bind, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 90 * time.Second}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(lab.Registry, promhttp.HandlerOpts{}))
	mon := &http.Server{Addr: cfg.MetricsBind, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	for _, s := range []*http.Server{web, mon} {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("serveur", "addr", s.Addr, "err", err)
				os.Exit(1)
			}
		}()
	}
	slog.Info("pklab démarré", "version", version, "web", cfg.Bind, "proxy", cfg.ProxyInternal)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = web.Shutdown(ctx)
	_ = mon.Shutdown(ctx)
}
