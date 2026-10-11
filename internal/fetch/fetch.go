// Package fetch télécharge les images sources avec des garde-fous :
// taille maximale, délai, redirections limitées et blocage des IP internes (SSRF).
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	ErrTooLarge     = errors.New("fichier source trop volumineux")
	ErrForbidden    = errors.New("source non autorisée")
	ErrNotFound     = errors.New("source introuvable")
	ErrUpstream     = errors.New("erreur du serveur source")
	ErrBadSourceURL = errors.New("URL source invalide")
)

type Fetcher struct {
	client       *http.Client
	maxBytes     int64
	allowedHosts []string
	localRoot    string
}

type Options struct {
	MaxBytes     int64
	Timeout      time.Duration
	AllowPrivate bool
	AllowedHosts []string
	LocalRoot    string
	MaxRedirects int
}

func New(o Options) *Fetcher {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	if !o.AllowPrivate {
		// Contrôle à la connexion (après résolution DNS) : empêche aussi le « DNS rebinding ».
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || IsPrivate(ip) {
				return fmt.Errorf("%w: adresse interne %s", ErrForbidden, host)
			}
			return nil
		}
	}
	tr := &http.Transport{
		DialContext:           dialer.DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   4 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		ForceAttemptHTTP2:     true,
	}
	maxRedir := o.MaxRedirects
	if maxRedir == 0 {
		maxRedir = 4
	}
	f := &Fetcher{maxBytes: o.MaxBytes, allowedHosts: o.AllowedHosts, localRoot: o.LocalRoot}
	f.client = &http.Client{
		Transport: tr,
		Timeout:   o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedir {
				return fmt.Errorf("%w: trop de redirections", ErrForbidden)
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// IsPrivate indique si l'IP est interne (loopback, privée, lien local, multicast...).
func IsPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		cgnat.Contains(ip) || ip.Equal(net.IPv4bcast)
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func (f *Fetcher) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: schéma %q", ErrForbidden, u.Scheme)
	}
	if len(f.allowedHosts) == 0 {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range f.allowedHosts {
		if host == h || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			return nil
		}
	}
	return fmt.Errorf("%w: domaine %s non autorisé", ErrForbidden, host)
}

// Get renvoie le contenu de la source (http(s):// ou local:///chemin).
func (f *Fetcher) Get(ctx context.Context, src string) ([]byte, error) {
	if rel, ok := strings.CutPrefix(src, "local://"); ok {
		return f.local(rel)
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return nil, ErrBadSourceURL
	}
	if err := f.checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrBadSourceURL
	}
	req.Header.Set("User-Agent", "proxykenny/1.0")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return nil, ErrForbidden
		}
		return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: statut %d", ErrUpstream, resp.StatusCode)
	case resp.ContentLength > f.maxBytes:
		return nil, ErrTooLarge
	}
	return readLimited(resp.Body, resp.ContentLength, f.maxBytes)
}

func (f *Fetcher) local(rel string) ([]byte, error) {
	if f.localRoot == "" {
		return nil, ErrForbidden
	}
	p := filepath.Join(f.localRoot, filepath.Clean("/"+rel))
	if !strings.HasPrefix(p, filepath.Clean(f.localRoot)+string(os.PathSeparator)) {
		return nil, ErrForbidden
	}
	fh, err := os.Open(p)
	if err != nil {
		return nil, ErrNotFound
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil || st.IsDir() {
		return nil, ErrNotFound
	}
	if st.Size() > f.maxBytes {
		return nil, ErrTooLarge
	}
	return readLimited(fh, st.Size(), f.maxBytes)
}

func readLimited(r io.Reader, size, max int64) ([]byte, error) {
	capHint := size
	if capHint <= 0 || capHint > max {
		capHint = 512 << 10
	}
	buf := make([]byte, 0, capHint)
	lr := io.LimitReader(r, max+1)
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := lr.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if int64(len(buf)) > max {
			return nil, ErrTooLarge
		}
		if err == io.EOF {
			return buf, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUpstream, err)
		}
	}
}
