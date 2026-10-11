package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidbyttow/govips/v2/vips"

	"proxykenny/internal/config"
	"proxykenny/internal/process"
	"proxykenny/internal/security"
)

var key, salt = []byte("cle-de-test"), []byte("sel")

func TestMain(m *testing.M) {
	vips.LoggingSettings(func(string, vips.LogLevel, string) {}, vips.LogLevelError)
	if err := vips.Startup(&vips.Config{ConcurrencyLevel: 1}); err != nil {
		panic(err)
	}
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

func pngImage(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// newServer : serveur de test avec une image locale « a.png ».
func newServer(t *testing.T, tweak func(*config.Config)) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), pngImage(320, 240), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Key: key, Salt: salt, MaxSrcBytes: 10 << 20, MaxSrcPixels: 50_000_000, MaxDstDim: 4000,
		MaxFrames: 10, Workers: 2, MaxQueue: 4, MaxDownloads: 8, DownloadTimeout: 2 * time.Second,
		RequestTimeout: 5 * time.Second, LocalRoot: dir, AutoFormats: []string{"webp"},
		JpegQuality: 80, WebpQuality: 80, PngCompression: 6, FaceMaxSide: 640, FaceEngine: "pigo",
	}
	if tweak != nil {
		tweak(cfg)
	}
	p, err := process.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, p, "test")
}

func get(s *Server, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func signed(path string) string { return "/" + security.Sign(key, salt, path) + path }

func TestSignedRequest(t *testing.T) {
	s := newServer(t, nil)
	rec := get(s, signed("/rs:fit:100:0/f:webp/plain/local:///a.png"))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d : %s", rec.Code, rec.Body.String())
	}
	h := rec.Header()
	if h.Get("Content-Type") != "image/webp" || h.Get("X-Image-Size") != "100x75" ||
		h.Get("ETag") == "" || !strings.Contains(h.Get("Server-Timing"), "proc;dur=") {
		t.Errorf("en-têtes : %v", h)
	}
	// Même requête avec l'ETag : 304 sans corps (cache navigateur / CDN).
	again := get(s, signed("/rs:fit:100:0/f:webp/plain/local:///a.png"), "If-None-Match", h.Get("ETag"))
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("revalidation : %d, %d octets", again.Code, again.Body.Len())
	}
}

func TestRejections(t *testing.T) {
	s := newServer(t, nil)
	sig := security.Sign(key, salt, "/w:100/plain/local:///a.png")
	cases := []struct {
		path string
		code int
	}{
		{"/" + sig + "/w:101/plain/local:///a.png", http.StatusForbidden},       // signature d'un autre chemin
		{"/unsafe/w:100/plain/local:///a.png", http.StatusForbidden},            // non signé, interdit par défaut
		{signed("/w:99999/plain/local:///a.png"), http.StatusBadRequest},        // dimension hors limites
		{signed("/w:100/plain/local:///absent.png"), http.StatusNotFound},       // source introuvable
		{signed("/w:100/plain/http://127.0.0.1:9/x.png"), http.StatusForbidden}, // SSRF : IP interne
		{"/health", http.StatusOK},
	}
	for _, c := range cases {
		if rec := get(s, c.path); rec.Code != c.code {
			t.Errorf("%s : %d au lieu de %d (%s)", c.path, rec.Code, c.code, strings.TrimSpace(rec.Body.String()))
		}
	}
	if rec := get(newServer(t, func(c *config.Config) { c.AllowUnsigned = true }), "/unsafe/w:50/plain/local:///a.png"); rec.Code != http.StatusOK {
		t.Errorf("mode développement : unsafe refusé (%d)", rec.Code)
	}
}

// Surcharge : au-delà de Workers + MaxQueue requêtes en cours, réponse 503 après RejectDelay.
func TestOverload(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release // la source « bloque » : la requête occupe sa place
		_, _ = w.Write(pngImage(64, 64))
	}))
	defer slow.Close()
	s := newServer(t, func(c *config.Config) {
		c.Workers, c.MaxQueue, c.AllowPrivateSources, c.RejectDelay = 1, 0, true, 100*time.Millisecond
	})
	first := make(chan int)
	go func() { first <- get(s, signed("/w:32/plain/"+slow.URL+"/a.png")).Code }()
	time.Sleep(100 * time.Millisecond) // la première requête est en cours

	t0 := time.Now()
	rec := get(s, signed("/w:32/plain/local:///a.png"))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Errorf("surcharge : %d (503 attendu avec Retry-After)", rec.Code)
	}
	if d := time.Since(t0); d < 100*time.Millisecond {
		t.Errorf("503 renvoyé en %v : l'attente anti-rafale n'est pas appliquée", d)
	}
	close(release)
	if code := <-first; code != http.StatusOK {
		t.Errorf("première requête : %d", code)
	}
	if rec := get(s, signed("/w:32/plain/local:///a.png")); rec.Code != http.StatusOK {
		t.Errorf("après la charge : %d", rec.Code)
	}
}
