package lab

import (
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	lab   *Lab
	pages map[string]*template.Template
}

func NewServer(l *Lab) (*Server, error) {
	funcs := template.FuncMap{
		"bytes":   humanBytes,
		"pct":     func(f float64) string { return fmt.Sprintf("%.0f %%", f*100) },
		"ms":      func(f float64) string { return fmt.Sprintf("%.0f ms", f) },
		"msf":     func(f float64) string { return fmt.Sprintf("%.1f", f) },
		"url":     l.PublicURL,
		"presets": func() []Preset { return Presets },
		"since":   func(t time.Time) string { return t.Format("02/01 15:04:05") },
	}
	s := &Server{lab: l, pages: map[string]*template.Template{}}
	funcs["secs"] = func(d time.Duration) string { return fmt.Sprintf("%.0f s", d.Seconds()) }
	for _, p := range []string{"index", "image", "play", "bench", "campaign"} {
		t, err := template.New("").Funcs(funcs).ParseFS(webFS, "web/layout.html", "web/"+p+".html")
		if err != nil {
			return nil, fmt.Errorf("gabarit %s: %w", p, err)
		}
		s.pages[p] = t
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /upload", s.upload)
	mux.HandleFunc("GET /image", s.image)
	mux.HandleFunc("GET /play", s.play)
	mux.HandleFunc("GET /bench", s.bench)
	mux.HandleFunc("POST /bench", s.bench)
	mux.HandleFunc("POST /campaign", s.startCampaign)
	mux.HandleFunc("GET /campaign", s.campaign)
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /static/", http.FileServerFS(static))
	return s.secure(mux)
}

// secure ajoute l'authentification basique et des en-têtes de sécurité.
func (s *Server) secure(next http.Handler) http.Handler {
	csp := "default-src 'self'; img-src 'self' data: " + s.lab.cfg.ProxyPublic +
		"; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		if s.lab.cfg.Password != "" {
			u, p, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(s.lab.cfg.User)) != 1 ||
				subtle.ConstantTimeCompare([]byte(p), []byte(s.lab.cfg.Password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="proxykenny lab", charset="UTF-8"`)
				http.Error(w, "authentification requise", http.StatusUnauthorized)
				return
			}
		}
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type page struct {
	Title   string
	Active  string
	Grafana string
	Flash   string
	Refresh int // > 0 : la page se recharge toute seule (campagne en cours)
	Data    any
}

func (s *Server) render(w http.ResponseWriter, name string, p page) {
	p.Grafana = s.lab.cfg.GrafanaURL
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[name].ExecuteTemplate(w, "layout", p); err != nil {
		slog.Error("rendu", "page", name, "err", err)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index", page{Title: "Galerie", Active: "index", Flash: r.URL.Query().Get("msg"), Data: s.lab.Images()})
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.lab.cfg.MaxUploadMB)<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Envoi impossible : fichier absent ou trop gros (max "+strconv.Itoa(s.lab.cfg.MaxUploadMB)+" Mo)"), http.StatusSeeOther)
		return
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(hdr.Filename))
	if !allowedExt[ext] {
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Extension non acceptée : "+ext), http.StatusSeeOther)
		return
	}
	stem := strings.Trim(unsafeChars.ReplaceAllString(strings.TrimSuffix(filepath.Base(hdr.Filename), filepath.Ext(hdr.Filename)), "-"), "-.")
	if stem == "" {
		stem = "image"
	}
	stem = stem[:min(len(stem), 60)]
	dir := filepath.Join(s.lab.cfg.ImagesDir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "dossier d'envoi inaccessible", http.StatusInternalServerError)
		return
	}
	var out *os.File
	var name string
	for i := 0; i < 100; i++ {
		name = stem + ext
		if i > 0 {
			name = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		if out, err = os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
			break
		}
	}
	if out == nil {
		http.Error(w, "impossible d'enregistrer le fichier", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		os.Remove(out.Name())
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Envoi interrompu"), http.StatusSeeOther)
		return
	}
	out.Close()
	uploadsTotal.Inc()
	http.Redirect(w, r, "/image?f="+url.QueryEscape("uploads/"+name), http.StatusSeeOther)
}

type imageData struct {
	Image   Image
	Results []Result
	Best    *Result
}

func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	img, ok := s.lab.Lookup(r.URL.Query().Get("f"))
	if !ok {
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Image introuvable"), http.StatusSeeOther)
		return
	}
	res := s.lab.Suite(r.Context(), img, r.URL.Query().Get("refresh") == "1")
	d := imageData{Image: img, Results: res}
	for i := range res {
		if res[i].Err == "" && strings.HasPrefix(res[i].Preset.ID, "fit-") && (d.Best == nil || res[i].Bytes < d.Best.Bytes) {
			d.Best = &res[i]
		}
	}
	s.render(w, "image", page{Title: img.Rel, Active: "index", Data: d})
}

type playData struct {
	Images []Image
	Image  Image
	Q      url.Values
	Opts   string
	Path   string
	Result *Result
}

func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	d := playData{Images: s.lab.Images(), Q: q}
	if len(d.Images) == 0 {
		s.render(w, "play", page{Title: "Bac à sable", Active: "play", Data: d})
		return
	}
	img, ok := s.lab.Lookup(q.Get("f"))
	if !ok {
		img = d.Images[0]
	}
	d.Image = img
	d.Opts = BuildOpts(q)
	d.Path = s.lab.Path(d.Opts, img.Rel)
	res := s.lab.Measure(r.Context(), d.Opts, img)
	d.Result = &res
	s.render(w, "play", page{Title: "Bac à sable", Active: "play", Data: d})
}

// BuildOpts construit les options proxykenny depuis le formulaire, valeurs bornées.
func BuildOpts(q url.Values) string {
	pick := func(v string, allowed ...string) string {
		for _, a := range allowed {
			if v == a {
				return v
			}
		}
		return ""
	}
	num := func(k string, lo, hi float64) float64 {
		f, err := strconv.ParseFloat(q.Get(k), 64)
		if err != nil {
			return 0
		}
		return max(lo, min(f, hi))
	}
	var parts []string
	// Recadrage préalable (c:) : pixels, ou proportion si ≤ 1.
	if cw, ch := num("cw", 0, 20000), num("ch", 0, 20000); cw > 0 || ch > 0 {
		c := "c:" + strconv.FormatFloat(cw, 'f', -1, 64) + ":" + strconv.FormatFloat(ch, 'f', -1, 64)
		if cg := pick(q.Get("cg"), "no", "so", "ea", "we", "noea", "nowe", "soea", "sowe", "sm", "face"); cg != "" {
			c += ":" + cg
		}
		parts = append(parts, c)
	}
	rt := pick(q.Get("rt"), "fit", "fill", "force", "auto")
	if rt == "" {
		rt = "fit"
	}
	w, h := int(num("w", 0, 4000)), int(num("h", 0, 4000))
	g := pick(q.Get("g"), "no", "so", "ea", "we", "noea", "nowe", "soea", "sowe", "sm", "face", "fp")
	fz := num("fc", 0, 5)
	// La gravité ne sert qu'en recadrant : elle implique le mode fill avec les deux dimensions.
	if g != "" && fz == 0 {
		rt = "fill"
		if w == 0 {
			w = h
		}
		if h == 0 {
			h = w
		}
	}
	if w > 0 || h > 0 {
		parts = append(parts, fmt.Sprintf("rs:%s:%d:%d", rt, w, h))
	}
	switch {
	case fz > 0:
		fc := "fc:" + strconv.FormatFloat(fz, 'f', -1, 64)
		if q.Get("fcl") != "" {
			fc += ":largest"
		}
		parts = append(parts, fc)
	case g == "fp":
		parts = append(parts, fmt.Sprintf("g:fp:%s:%s", strconv.FormatFloat(num("fx", 0, 1), 'f', -1, 64), strconv.FormatFloat(num("fy", 0, 1), 'f', -1, 64)))
	case g != "":
		parts = append(parts, "g:"+g)
	}
	if q.Get("fd") != "" {
		parts = append(parts, "fd")
	}
	for _, k := range []struct {
		key, opt string
		hi       float64
	}{{"bl", "bl", 50}, {"sh", "sh", 5}, {"pix", "pix", 100}, {"bf", "bf", 50}, {"pf", "pf", 100}, {"q", "q", 100}} {
		if v := num(k.key, 0, k.hi); v > 0 {
			parts = append(parts, k.opt+":"+strconv.FormatFloat(v, 'f', -1, 64))
		}
	}
	if rot := pick(q.Get("rot"), "90", "180", "270"); rot != "" {
		parts = append(parts, "rot:"+rot)
	}
	if f := pick(q.Get("fmt"), "jpeg", "png", "webp", "avif", "jxl", "gif", "tiff", "auto"); f != "" {
		parts = append(parts, "f:"+f)
	}
	if len(parts) == 0 {
		return "w:800"
	}
	return strings.Join(parts, "/")
}

type benchData struct {
	Images    []Image
	History   []*BenchResult
	Last      *BenchResult
	Error     string
	MaxConc   int
	MaxDur    int
	Campaigns []CampaignView
	Steps     int
}

func (s *Server) startCampaign(w http.ResponseWriter, r *http.Request) {
	img, ok := s.lab.Lookup(r.FormValue("f"))
	if !ok {
		http.Redirect(w, r, "/bench?err="+url.QueryEscape("image inconnue"), http.StatusSeeOther)
		return
	}
	dur, _ := strconv.Atoi(r.FormValue("d"))
	c, err := s.lab.StartCampaign(img, time.Duration(dur)*time.Second)
	if err != nil {
		http.Redirect(w, r, "/bench?err="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/campaign?id="+c.ID, http.StatusSeeOther)
}

func (s *Server) campaign(w http.ResponseWriter, r *http.Request) {
	c := s.lab.Campaign(r.URL.Query().Get("id"))
	if c == nil {
		http.Redirect(w, r, "/bench", http.StatusSeeOther)
		return
	}
	v := c.View()
	p := page{Title: "Campagne de tests", Active: "bench", Data: v}
	if v.Running {
		p.Refresh = 3
	}
	s.render(w, "campaign", p)
}

func (s *Server) bench(w http.ResponseWriter, r *http.Request) {
	d := benchData{Images: s.lab.Images(), MaxConc: MaxBenchConcurrency, MaxDur: int(MaxBenchDuration.Seconds()),
		Error: r.URL.Query().Get("err"), Steps: len(DefaultCampaign())}
	s.lab.campMu.Lock()
	for _, c := range s.lab.campaigns {
		d.Campaigns = append(d.Campaigns, c.View())
	}
	s.lab.campMu.Unlock()
	if r.Method == http.MethodPost {
		img, ok := s.lab.Lookup(r.FormValue("f"))
		p, okp := PresetByID(r.FormValue("preset"))
		conc, _ := strconv.Atoi(r.FormValue("c"))
		dur, _ := strconv.Atoi(r.FormValue("d"))
		switch {
		case !ok || !okp:
			d.Error = "image ou scénario inconnu"
		default:
			res, err := s.lab.Bench(r.Context(), img, p, conc, time.Duration(dur)*time.Second)
			if err != nil {
				d.Error = err.Error()
			}
			d.Last = res
		}
	}
	d.History = s.lab.History()
	s.render(w, "bench", page{Title: "Bancs d'essai", Active: "bench", Data: d})
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f Mo", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f Ko", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d o", n)
}
