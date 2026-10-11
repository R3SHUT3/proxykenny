package fetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsPrivate(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "10.1.2.3": true, "192.168.1.100": true, "172.16.0.1": true,
		"169.254.169.254": true, "100.64.0.1": true, "::1": true, "fe80::1": true, "0.0.0.0": true,
		"8.8.8.8": false, "1.1.1.1": false, "2606:4700::1111": false,
	} {
		if got := IsPrivate(net.ParseIP(ip)); got != want {
			t.Errorf("%s : %v, attendu %v", ip, got, want)
		}
	}
}

func TestSSRFBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("x")) }))
	defer srv.Close()
	f := New(Options{MaxBytes: 1 << 20, Timeout: 2 * time.Second})
	if _, err := f.Get(context.Background(), srv.URL); !errors.Is(err, ErrForbidden) {
		t.Fatalf("accès à 127.0.0.1 non bloqué : %v", err)
	}
}

func TestSizeLimit(t *testing.T) {
	big := make([]byte, 2048)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(big) }))
	defer srv.Close()
	f := New(Options{MaxBytes: 1024, Timeout: 2 * time.Second, AllowPrivate: true})
	if _, err := f.Get(context.Background(), srv.URL); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("taille non limitée : %v", err)
	}
	f = New(Options{MaxBytes: 4096, Timeout: 2 * time.Second, AllowPrivate: true})
	if b, err := f.Get(context.Background(), srv.URL); err != nil || len(b) != 2048 {
		t.Fatalf("lecture : %d octets, %v", len(b), err)
	}
}

func TestLocalTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.jpg"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := New(Options{MaxBytes: 1 << 20, Timeout: time.Second, LocalRoot: dir})
	if b, err := f.Get(context.Background(), "local:///ok.jpg"); err != nil || string(b) != "abc" {
		t.Fatalf("lecture locale : %q %v", b, err)
	}
	if _, err := f.Get(context.Background(), "local:///../../etc/passwd"); err == nil {
		t.Fatal("sortie de la racine locale non bloquée")
	}
}

func TestAllowedHosts(t *testing.T) {
	f := New(Options{MaxBytes: 1, Timeout: time.Second, AllowedHosts: []string{".exemple.com"}})
	if _, err := f.Get(context.Background(), "https://evil.com/a.jpg"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("domaine non autorisé accepté : %v", err)
	}
}
