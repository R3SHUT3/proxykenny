// Package config charge la configuration depuis les variables d'environnement KP_*.
package config

import (
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Bind        string // API publique
	MetricsBind string // /metrics Prometheus (ne pas exposer publiquement)

	Key           []byte // clé HMAC (hex)
	Salt          []byte // sel HMAC (hex)
	AllowUnsigned bool   // accepte la signature "unsafe" (dev uniquement)

	// Protection de la machine
	MaxSrcBytes     int64         // taille max du fichier source
	MaxSrcPixels    int           // nb max de pixels (toutes pages) : anti « bombe de décompression »
	MaxDstDim       int           // largeur/hauteur max demandée
	MaxFrames       int           // nb max d'images d'une animation
	Workers         int           // traitements simultanés (≈ nb de cœurs)
	MaxQueue        int           // requêtes en attente avant de répondre 503
	MaxDownloads    int           // téléchargements simultanés
	DownloadTimeout time.Duration // délai max de téléchargement
	RequestTimeout  time.Duration // délai max total d'une requête
	RejectDelay     time.Duration // attente avant un 503 : freine les clients qui insistent

	// Sources
	AllowPrivateSources bool     // autorise les IP privées (SSRF) — désactivé par défaut
	AllowedHosts        []string // si non vide : seuls ces domaines sont autorisés
	LocalRoot           string   // racine des sources local:/// (vide = désactivé)

	// Encodage
	AutoFormats    []string // préférence pour f:auto selon l'en-tête Accept
	JpegQuality    int
	WebpQuality    int
	AvifQuality    int
	JxlQuality     int
	AvifEffort     int // 0 (rapide) .. 9 (lent)
	JxlEffort      int // 1 (rapide) .. 9 (lent)
	WebpEffort     int // 0 (rapide) .. 6 (lent)
	PngCompression int // 0..9

	FaceMaxSide    int     // taille de l'image réduite utilisée pour la détection de visages
	FaceMinQuality float64 // score minimal d'une détection (vrais visages > 100, faux positifs < 20)
	FaceTilt       string  // pigo — têtes inclinées : off, fallback (défaut), always
	FaceEngine     string  // yunet (défaut) ou pigo
	FaceScore      float64 // yunet : score minimal (0..1)
	ORTLib         string  // chemin de libonnxruntime.so
}

func Load() (*Config, error) {
	c := &Config{
		Bind:                env("KP_BIND", ":8080"),
		MetricsBind:         env("KP_METRICS_BIND", ":8081"),
		AllowUnsigned:       envBool("KP_ALLOW_UNSIGNED", false),
		MaxSrcBytes:         int64(envInt("KP_MAX_SRC_MB", 30)) << 20,
		MaxSrcPixels:        envInt("KP_MAX_SRC_MPIX", 60) * 1_000_000,
		MaxDstDim:           envInt("KP_MAX_DST_DIM", 8192),
		MaxFrames:           envInt("KP_MAX_FRAMES", 100),
		Workers:             envInt("KP_WORKERS", runtime.NumCPU()),
		MaxQueue:            envInt("KP_MAX_QUEUE", 64),
		DownloadTimeout:     envDur("KP_DOWNLOAD_TIMEOUT", 8*time.Second),
		RequestTimeout:      envDur("KP_REQUEST_TIMEOUT", 20*time.Second),
		RejectDelay:         envDur("KP_REJECT_DELAY", 250*time.Millisecond),
		AllowPrivateSources: envBool("KP_ALLOW_PRIVATE_SOURCES", false),
		AllowedHosts:        envList("KP_ALLOWED_HOSTS"),
		LocalRoot:           env("KP_LOCAL_ROOT", ""),
		AutoFormats:         envListDefault("KP_AUTO_FORMATS", []string{"webp", "avif"}),
		JpegQuality:         envInt("KP_JPEG_QUALITY", 80),
		WebpQuality:         envInt("KP_WEBP_QUALITY", 80),
		AvifQuality:         envInt("KP_AVIF_QUALITY", 60),
		JxlQuality:          envInt("KP_JXL_QUALITY", 80),
		AvifEffort:          envInt("KP_AVIF_EFFORT", 2),
		JxlEffort:           envInt("KP_JXL_EFFORT", 3),
		WebpEffort:          envInt("KP_WEBP_EFFORT", 3),
		PngCompression:      envInt("KP_PNG_COMPRESSION", 6),
		FaceMaxSide:         envInt("KP_FACE_MAX_SIDE", 640),
		FaceMinQuality:      float64(envInt("KP_FACE_MIN_QUALITY", 30)),
		FaceTilt:            env("KP_FACE_TILT", "fallback"),
		FaceEngine:          env("KP_FACE_ENGINE", "yunet"),
		FaceScore:           envFloat("KP_FACE_SCORE", 0.6),
		ORTLib:              env("KP_ORT_LIB", "/usr/local/lib/onnxruntime/libonnxruntime.so"),
	}
	c.MaxDownloads = envInt("KP_MAX_DOWNLOADS", c.Workers*4)

	var err error
	if c.Key, err = hex.DecodeString(os.Getenv("KP_KEY")); err != nil {
		return nil, fmt.Errorf("KP_KEY doit être en hexadécimal: %w", err)
	}
	if c.Salt, err = hex.DecodeString(os.Getenv("KP_SALT")); err != nil {
		return nil, fmt.Errorf("KP_SALT doit être en hexadécimal: %w", err)
	}
	if len(c.Key) == 0 && !c.AllowUnsigned {
		return nil, fmt.Errorf("KP_KEY vide : définir une clé ou KP_ALLOW_UNSIGNED=true (dev)")
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envList(k string) []string {
	var out []string
	for _, s := range strings.Split(os.Getenv(k), ",") {
		if s = strings.TrimSpace(strings.ToLower(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func envListDefault(k string, def []string) []string {
	if l := envList(k); len(l) > 0 {
		return l
	}
	return def
}
