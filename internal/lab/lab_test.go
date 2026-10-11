package lab

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildOpts(t *testing.T) {
	cases := map[string]string{
		"":                                    "w:800",
		"rt=fill&w=400&h=300&g=face&fmt=webp": "rs:fill:400:300/g:face/f:webp",
		"w=99999&bl=999&fmt=exe&g=x":          "rs:fit:4000:0/bl:50", // valeurs bornées, inconnues ignorées
		"w=300&bf=12&pf=10&rot=90&q=70":       "rs:fit:300:0/bf:12/pf:10/q:70/rot:90",
		"rt=fit&w=400&g=no":                   "rs:fill:400:400/g:no", // gravité ⇒ fill, carré si une seule dimension
		"w=256&h=256&fc=0.4&fcl=on&fd=on":     "rs:fit:256:256/fc:0.4:largest/fd",
		"cw=0.5&ch=0.5&cg=sowe&w=300":         "c:0.5:0.5:sowe/rs:fit:300:0",
		"w=300&h=200&g=fp&fx=0.2&fy=0.8":      "rs:fill:300:200/g:fp:0.2:0.8",
	}
	for in, want := range cases {
		q, _ := url.ParseQuery(in)
		if got := BuildOpts(q); got != want {
			t.Errorf("%q → %q, attendu %q", in, got, want)
		}
	}
}

func TestServerTiming(t *testing.T) {
	h := "dl;dur=1.1, proc;dur=19.5, total;dur=20.2"
	if v := serverTiming(h, "proc"); v != 19.5 {
		t.Errorf("proc = %v", v)
	}
	if v := serverTiming(h, "absent"); v != 0 {
		t.Errorf("absent = %v", v)
	}
}

func TestLookupRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "ok.jpg"), []byte("x"), 0o644)
	l := New(Config{ImagesDir: dir, Key: []byte("k")})
	if _, ok := l.Lookup("ok.jpg"); !ok {
		t.Error("image existante non trouvée")
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "ok.jpg/../../x", "a b.jpg", ""} {
		if _, ok := l.Lookup(bad); ok {
			t.Errorf("%q accepté", bad)
		}
	}
}

func TestAuthAndPages(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "photo.jpg"), []byte("x"), 0o644)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("X-Image-Size", "10x10")
		w.Header().Set("Server-Timing", "proc;dur=3.0")
		_, _ = w.Write([]byte("img"))
	}))
	defer proxy.Close()
	s, err := NewServer(New(Config{ImagesDir: dir, Key: []byte("k"), User: "admin", Password: "secret",
		ProxyInternal: proxy.URL, ProxyPublic: "http://pub:8080"}))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sans mot de passe : %d", rec.Code)
	}
	// Campagne courte : lancement, page de progression, puis résultats complets.
	l := s.lab
	img, _ := l.Lookup("photo.jpg")
	minStepDur, stepPause = 50*time.Millisecond, 0
	c, err := l.StartCampaign(img, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.StartCampaign(img, 50*time.Millisecond); err == nil {
		t.Error("deux campagnes simultanées acceptées")
	}
	<-c.finished
	v := c.View()
	if v.Done != len(DefaultCampaign()) || v.Running || len(v.Groups) != 4 || len(v.Summary) == 0 {
		t.Errorf("campagne : %d/%d tests, %d groupes, %d conclusions", v.Done, v.Total, len(v.Groups), len(v.Summary))
	}
	for _, path := range []string{"/", "/image?f=photo.jpg", "/play?f=photo.jpg&rt=fill&w=200&h=200&g=face", "/bench", "/static/style.css", "/campaign?id=" + c.ID} {
		req := httptest.NewRequest("GET", path, nil)
		req.SetBasicAuth("admin", "secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s : %d", path, rec.Code)
		}
		if strings.HasPrefix(path, "/image") && !strings.Contains(rec.Body.String(), "http://pub:8080/") {
			t.Errorf("%s : URL publique absente", path)
		}
	}
}
