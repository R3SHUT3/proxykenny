// Package server expose l'API HTTP : GET /<signature>/<options>/<source>.
//
// Protection de la machine :
//   - au plus Workers traitements simultanés (CPU) et MaxDownloads téléchargements ;
//   - au-delà de MaxQueue requêtes en attente : réponse 503 immédiate (pas d'emballement) ;
//   - délai maximal par requête.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"proxykenny/internal/config"
	"proxykenny/internal/fetch"
	"proxykenny/internal/metrics"
	"proxykenny/internal/options"
	"proxykenny/internal/process"
	"proxykenny/internal/security"
)

type Server struct {
	cfg       *config.Config
	fetcher   *fetch.Fetcher
	proc      *process.Processor
	workers   chan struct{}
	downloads chan struct{}
	waiting   atomic.Int64
	version   string
}

func New(cfg *config.Config, proc *process.Processor, version string) *Server {
	return &Server{
		cfg:  cfg,
		proc: proc,
		fetcher: fetch.New(fetch.Options{
			MaxBytes: cfg.MaxSrcBytes, Timeout: cfg.DownloadTimeout, AllowPrivate: cfg.AllowPrivateSources,
			AllowedHosts: cfg.AllowedHosts, LocalRoot: cfg.LocalRoot,
		}),
		workers:   make(chan struct{}, cfg.Workers),
		downloads: make(chan struct{}, cfg.MaxDownloads),
		version:   version,
	}
}

type httpError struct {
	code   int
	reason string
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func fail(code int, reason, msg string) *httpError { return &httpError{code, reason, msg} }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	code := s.handle(w, r, start)
	metrics.Requests.WithLabelValues(strconv.Itoa(code)).Inc()
	// « total » = images réellement servies ; les refus (souvent instantanés) sont mesurés à part
	// pour ne pas fausser le temps de réponse.
	stage := "total"
	if code != http.StatusOK && code != http.StatusNotModified {
		stage = "rejected"
	}
	metrics.Duration.WithLabelValues(stage).Observe(time.Since(start).Seconds())
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, start time.Time) int {
	switch r.URL.Path {
	case "/health", "/healthz":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
		return http.StatusOK
	case "/", "/favicon.ico", "/robots.txt":
		http.NotFound(w, r)
		return http.StatusNotFound
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		return s.writeErr(w, fail(http.StatusMethodNotAllowed, "method", "méthode non autorisée"))
	}

	// Chemin brut : la signature porte sur le chemin tel qu'envoyé.
	raw := r.URL.EscapedPath()
	sig, path, ok := strings.Cut(strings.TrimPrefix(raw, "/"), "/")
	if !ok {
		return s.writeErr(w, fail(http.StatusNotFound, "path", "chemin invalide"))
	}
	path = "/" + path
	if !(s.cfg.AllowUnsigned && (sig == "unsafe" || sig == "_" || sig == "insecure")) &&
		!security.Verify(s.cfg.Key, s.cfg.Salt, sig, path) {
		return s.writeErr(w, fail(http.StatusForbidden, "signature", "signature invalide"))
	}

	o, src, err := options.Parse(path, options.Limits{MaxDim: s.cfg.MaxDstDim})
	if err != nil {
		return s.writeErr(w, fail(http.StatusBadRequest, "options", err.Error()))
	}

	// Réponse déterministe pour une URL donnée : ETag + cache navigateur/CDN.
	accept := r.Header.Get("Accept")
	etagSrc := s.version + path
	if o.Format == "auto" {
		etagSrc += "|" + accept
		w.Header().Set("Vary", "Accept")
	}
	sum := sha256.Sum256([]byte(etagSrc))
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	defer cancel()

	// File d'attente bornée : on refuse vite plutôt que de saturer la machine.
	if n := s.waiting.Add(1); n > int64(s.cfg.MaxQueue)+int64(s.cfg.Workers) {
		s.waiting.Add(-1)
		// On fait patienter avant de refuser : une attente ne coûte pas de CPU, alors qu'un
		// client qui reçoit des 503 instantanés en boucle monopoliserait le processeur.
		if s.cfg.RejectDelay > 0 {
			select {
			case <-time.After(s.cfg.RejectDelay):
			case <-r.Context().Done():
			}
		}
		w.Header().Set("Retry-After", "1")
		return s.writeErr(w, fail(http.StatusServiceUnavailable, "overloaded", "serveur saturé, réessayer"))
	}
	metrics.Waiting.Inc()
	defer func() { s.waiting.Add(-1) }()

	// Téléchargement (I/O) : limité séparément du CPU.
	tDl := time.Now()
	select {
	case s.downloads <- struct{}{}:
	case <-ctx.Done():
		metrics.Waiting.Dec()
		return s.writeErr(w, fail(http.StatusServiceUnavailable, "queue_timeout", "délai d'attente dépassé"))
	}
	data, err := s.fetcher.Get(ctx, src)
	<-s.downloads
	metrics.Duration.WithLabelValues("download").Observe(time.Since(tDl).Seconds())
	if err != nil {
		metrics.Waiting.Dec()
		return s.writeErr(w, fetchErr(err))
	}
	metrics.SrcBytes.Observe(float64(len(data)))

	// Traitement (CPU) : au plus Workers en parallèle.
	tQ := time.Now()
	select {
	case s.workers <- struct{}{}:
	case <-ctx.Done():
		metrics.Waiting.Dec()
		return s.writeErr(w, fail(http.StatusServiceUnavailable, "queue_timeout", "délai d'attente dépassé"))
	}
	metrics.Waiting.Dec()
	metrics.Duration.WithLabelValues("queue").Observe(time.Since(tQ).Seconds())
	metrics.InFlight.Inc()
	tP := time.Now()
	res, err := s.proc.Process(data, o, accept)
	metrics.InFlight.Dec()
	<-s.workers
	procDur := time.Since(tP)
	metrics.Duration.WithLabelValues("process").Observe(procDur.Seconds())
	if err != nil {
		return s.writeErr(w, procErr(err))
	}
	if ctx.Err() != nil {
		return s.writeErr(w, fail(http.StatusGatewayTimeout, "timeout", "délai dépassé"))
	}

	metrics.Images.WithLabelValues(res.SrcFormat, res.Format).Inc()
	metrics.OutBytes.Observe(float64(len(res.Body)))
	if res.Faces > 0 {
		metrics.Faces.Add(float64(res.Faces))
	}

	h := w.Header()
	h.Set("Content-Type", res.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(res.Body)))
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Server-Timing", fmt.Sprintf("dl;dur=%.1f, proc;dur=%.1f, total;dur=%.1f",
		float64(tQ.Sub(tDl).Microseconds())/1000, float64(procDur.Microseconds())/1000,
		float64(time.Since(start).Microseconds())/1000))
	h.Set("X-Image-Size", fmt.Sprintf("%dx%d", res.Width, res.Height))
	if res.Faces >= 0 {
		h.Set("X-Faces", strconv.Itoa(res.Faces))
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(res.Body)
	}
	return http.StatusOK
}

func fetchErr(err error) *httpError {
	switch {
	case errors.Is(err, fetch.ErrTooLarge):
		return fail(http.StatusRequestEntityTooLarge, "src_too_large", err.Error())
	case errors.Is(err, fetch.ErrForbidden):
		return fail(http.StatusForbidden, "src_forbidden", "source non autorisée")
	case errors.Is(err, fetch.ErrNotFound):
		return fail(http.StatusNotFound, "src_not_found", err.Error())
	case errors.Is(err, fetch.ErrBadSourceURL):
		return fail(http.StatusBadRequest, "src_url", err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return fail(http.StatusGatewayTimeout, "src_timeout", "délai de téléchargement dépassé")
	}
	return fail(http.StatusBadGateway, "src_error", err.Error())
}

func procErr(err error) *httpError {
	switch {
	case errors.Is(err, process.ErrTooManyPixels):
		return fail(http.StatusUnprocessableEntity, "too_many_pixels", err.Error())
	case errors.Is(err, process.ErrUnsupported):
		return fail(http.StatusUnsupportedMediaType, "unsupported", err.Error())
	}
	return fail(http.StatusInternalServerError, "processing", err.Error())
}

func (s *Server) writeErr(w http.ResponseWriter, e *httpError) int {
	metrics.Rejected.WithLabelValues(e.reason).Inc()
	if e.code >= 500 && e.code != http.StatusServiceUnavailable {
		slog.Warn("erreur", "code", e.code, "reason", e.reason, "err", e.msg)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.code)
	_, _ = w.Write([]byte(e.msg + "\n"))
	return e.code
}
