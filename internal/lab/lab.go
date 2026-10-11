// Package lab est la web app de démonstration de proxykenny : galerie, comparaison des
// transformations, bac à sable et bancs d'essai. Elle signe elle-même les URL (KP_KEY/KP_SALT).
package lab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxykenny/internal/security"
)

type Config struct {
	Bind          string
	MetricsBind   string
	ProxyInternal string // URL de proxykenny vue du lab (mesures, bancs d'essai)
	ProxyPublic   string // URL de proxykenny vue du navigateur (balises <img>)
	Key, Salt     []byte
	ImagesDir     string // même dossier que KP_LOCAL_ROOT de proxykenny
	User          string
	Password      string // authentification basique (vide = désactivée)
	GrafanaURL    string
	MaxUploadMB   int
}

// Preset est une transformation de démonstration.
type Preset struct {
	ID, Name, Opts string
}

var Presets = []Preset{
	{"ref-jpeg", "Référence JPEG 1200 px", "w:1200/f:jpeg"},
	{"fit-webp", "800 px → WebP", "rs:fit:800:800/f:webp"},
	{"fit-avif", "800 px → AVIF", "rs:fit:800:800/f:avif"},
	{"fit-jxl", "800 px → JPEG XL", "rs:fit:800:800/f:jxl"},
	{"fill-smart", "Vignette 400×400 « smart »", "rs:fill:400:400/g:sm/f:webp"},
	{"fill-face", "Vignette 400×400 « visage »", "rs:fill:400:400/g:face/f:webp"},
	{"face-boxes", "Visages détectés (cadres)", "w:1000/fd/f:webp"},
	{"face-portrait", "Portrait serré 400×500 (zoom visage)", "rs:fill:400:500/fc:0.6/f:webp"},
	{"face-avatar", "Avatar 256×256 (visage principal)", "rs:fill:256:256/fc:0.35:largest/f:webp"},
	{"top", "Vignette 400×400 ancrée en haut", "rs:fill:400:400/g:no/f:webp"},
	{"crop-half", "Moitié gauche de l'image", "c:0.5:1:we/w:600/f:webp"},
	{"banner", "Bannière 1200×300 « smart »", "rs:fill:1200:300/g:sm/f:avif"},
	{"blur-faces", "Visages floutés", "w:800/bf:12/f:webp"},
	{"pix-faces", "Visages pixellisés", "w:800/pf:16/f:webp"},
	{"blur", "Flou gaussien", "w:800/bl:6/f:webp"},
	{"sharpen", "Netteté renforcée", "w:800/sh:1.5/f:webp"},
	{"pixelate", "Pixellisation", "w:800/pix:14/f:webp"},
	{"crop-rot", "Crop centre + rotation 90°", "c:1000:1000/w:500/rot:90/f:webp"},
	{"gray-bg", "Transparence aplatie sur fond", "w:800/bg:1f2937/f:jpeg"},
}

func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

type Lab struct {
	cfg    Config
	client *http.Client

	mu     sync.Mutex
	suites map[string]suiteEntry

	benchMu sync.Mutex
	histMu  sync.Mutex
	history []*BenchResult

	campMu    sync.Mutex
	campaigns []*Campaign
}

type suiteEntry struct {
	at      time.Time
	results []Result
}

func New(cfg Config) *Lab {
	return &Lab{
		cfg:    cfg,
		client: &http.Client{Timeout: 30 * time.Second},
		suites: map[string]suiteEntry{},
	}
}

// Path renvoie le chemin signé ("/<sig>/<opts>/plain/local:///<rel>").
func (l *Lab) Path(opts, rel string) string {
	p := "/" + strings.Trim(opts, "/") + "/plain/local:///" + rel
	return "/" + security.Sign(l.cfg.Key, l.cfg.Salt, p) + p
}

// PublicURL est l'URL utilisable par le navigateur.
func (l *Lab) PublicURL(opts, rel string) string { return l.cfg.ProxyPublic + l.Path(opts, rel) }

// Image source disponible.
type Image struct {
	Rel   string
	Bytes int64
	Mod   time.Time
}

var allowedExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".avif": true, ".heic": true, ".heif": true,
	".jxl": true, ".gif": true, ".tif": true, ".tiff": true, ".svg": true, ".pdf": true, ".bmp": true,
	".ico": true, ".psd": true, ".jp2": true, ".exr": true,
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)?$`)

// Images liste les images du dossier (et du sous-dossier uploads/).
func (l *Lab) Images() []Image {
	var out []Image
	for _, dir := range []string{"", "uploads"} {
		entries, err := os.ReadDir(filepath.Join(l.cfg.ImagesDir, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel := filepath.ToSlash(filepath.Join(dir, e.Name()))
			if e.IsDir() || !allowedExt[strings.ToLower(filepath.Ext(e.Name()))] || !safeName.MatchString(rel) {
				continue
			}
			if info, err := e.Info(); err == nil {
				out = append(out, Image{Rel: rel, Bytes: info.Size(), Mod: info.ModTime()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mod.After(out[j].Mod) })
	return out
}

// Lookup vérifie qu'une image existe (protège des chemins arbitraires).
func (l *Lab) Lookup(rel string) (Image, bool) {
	if !safeName.MatchString(rel) {
		return Image{}, false
	}
	for _, im := range l.Images() {
		if im.Rel == rel {
			return im, true
		}
	}
	return Image{}, false
}

// Result est la mesure d'une transformation.
type Result struct {
	Preset   Preset
	Opts     string
	URL      string // URL publique (affichage)
	Status   int
	Bytes    int64
	Format   string
	Size     string
	ProcMs   float64
	TotalMs  float64
	Faces    int
	Err      string
	SrcBytes int64
}

// Ratio : taille de sortie / taille source.
func (r Result) Ratio() float64 {
	if r.SrcBytes == 0 {
		return 0
	}
	return float64(r.Bytes) / float64(r.SrcBytes)
}

// Measure appelle proxykenny et relève format, taille, dimensions et temps serveur.
func (l *Lab) Measure(ctx context.Context, opts string, img Image) Result {
	res := Result{Opts: opts, URL: l.PublicURL(opts, img.Rel), SrcBytes: img.Bytes, Faces: -1}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.cfg.ProxyInternal+l.Path(opts, img.Rel), nil)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/*,*/*")
	t0 := time.Now()
	resp, err := l.client.Do(req)
	if err != nil {
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	res.TotalMs = float64(time.Since(t0).Microseconds()) / 1000
	res.Status, res.Bytes = resp.StatusCode, n
	if resp.StatusCode != http.StatusOK {
		res.Err = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return res
	}
	res.Format = strings.TrimPrefix(resp.Header.Get("Content-Type"), "image/")
	res.Size = resp.Header.Get("X-Image-Size")
	if f, err := strconv.Atoi(resp.Header.Get("X-Faces")); err == nil {
		res.Faces = f
	}
	res.ProcMs = serverTiming(resp.Header.Get("Server-Timing"), "proc")
	return res
}

func serverTiming(h, name string) float64 {
	for _, part := range strings.Split(h, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ";dur=")
		if ok && k == name {
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
	}
	return 0
}

// Suite mesure tous les presets pour une image (résultats gardés 10 minutes).
func (l *Lab) Suite(ctx context.Context, img Image, refresh bool) []Result {
	key := img.Rel + "|" + img.Mod.String()
	l.mu.Lock()
	if e, ok := l.suites[key]; ok && !refresh && time.Since(e.at) < 10*time.Minute {
		l.mu.Unlock()
		return e.results
	}
	l.mu.Unlock()

	results := make([]Result, len(Presets))
	sem := make(chan struct{}, 3) // ne pas monopoliser l'API pendant une démo
	var wg sync.WaitGroup
	for i, p := range Presets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := l.Measure(ctx, p.Opts, img)
			r.Preset = p
			results[i] = r
		}()
	}
	wg.Wait()
	suitesTotal.Inc()

	l.mu.Lock()
	l.suites[key] = suiteEntry{at: time.Now(), results: results}
	l.mu.Unlock()
	return results
}
