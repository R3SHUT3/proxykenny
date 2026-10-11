// proxykenny : serveur de traitement d'images à la volée (libvips).
//
//	proxykenny                 démarre le serveur
//	proxykenny -sign <chemin>  affiche l'URL signée d'un chemin (utilise KP_KEY / KP_SALT)
//	proxykenny -healthcheck    vérifie que le serveur local répond (HEALTHCHECK Docker)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"proxykenny/internal/config"
	"proxykenny/internal/metrics"
	"proxykenny/internal/process"
	"proxykenny/internal/security"
	"proxykenny/internal/server"
)

var version = "dev"

func main() {
	sign := flag.String("sign", "", "chemin à signer, ex. /rs:fit:300:0/plain/https://exemple.com/a.jpg")
	health := flag.Bool("healthcheck", false, "vérifie que le serveur local répond")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if *health {
		os.Exit(healthcheck())
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		os.Exit(2)
	}
	if *sign != "" {
		fmt.Println("/" + security.Sign(cfg.Key, cfg.Salt, *sign) + *sign)
		return
	}

	// libvips : 1 thread par opération (le parallélisme vient des workers), pas de cache
	// d'opérations (chaque image est différente) → mémoire minimale et prévisible.
	vips.LoggingSettings(func(string, vips.LogLevel, string) {}, vips.LogLevelError)
	if err := vips.Startup(&vips.Config{ConcurrencyLevel: 1, MaxCacheFiles: 0, MaxCacheMem: 0, MaxCacheSize: 0}); err != nil {
		slog.Error("démarrage libvips", "err", err)
		os.Exit(1)
	}
	defer vips.Shutdown()

	proc, err := process.New(cfg)
	if err != nil {
		slog.Error("initialisation", "err", err)
		os.Exit(1)
	}
	metrics.Init(version, cfg.Workers, cfg.MaxQueue)
	metrics.FaceEngine.WithLabelValues(proc.FaceEngine()).Set(1)
	if cfg.AllowUnsigned {
		slog.Warn("KP_ALLOW_UNSIGNED actif : URLs non signées acceptées (à réserver au développement)")
	}

	api := &http.Server{
		Addr:              cfg.Bind,
		Handler:           server.New(cfg, proc, version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 10*time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mon := &http.Server{Addr: cfg.MetricsBind, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go serve(mon, "métriques")
	go serve(api, "api")
	slog.Info("proxykenny démarré", "version", version, "api", cfg.Bind, "metrics", cfg.MetricsBind,
		"workers", cfg.Workers, "queue", cfg.MaxQueue, "max_src_mpix", cfg.MaxSrcPixels/1_000_000,
		"visages", proc.FaceEngine())

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("arrêt en cours")
	ctx, cancel := context.WithTimeout(context.Background(), cfg.RequestTimeout)
	defer cancel()
	_ = api.Shutdown(ctx)
	_ = mon.Shutdown(ctx)
}

func serve(s *http.Server, name string) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serveur "+name, "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("KP_BIND")
	if addr == "" || addr[0] == ':' {
		addr = "127.0.0.1" + map[bool]string{true: addr, false: ":8080"}[addr != ""]
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
