package process

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/davidbyttow/govips/v2/vips"

	"proxykenny/internal/config"
	"proxykenny/internal/face"
	"proxykenny/internal/options"
)

var (
	proc     *Processor
	fixtures = map[string][]byte{}
)

func TestMain(m *testing.M) {
	vips.LoggingSettings(func(string, vips.LogLevel, string) {}, vips.LogLevelError)
	if err := vips.Startup(&vips.Config{ConcurrencyLevel: 1}); err != nil {
		panic(err)
	}
	ortLib := os.Getenv("KP_ORT_LIB")
	if ortLib == "" {
		ortLib = "/usr/local/lib/onnxruntime/libonnxruntime.so"
	}
	cfg := &config.Config{
		MaxSrcPixels: 60_000_000, MaxFrames: 100, JpegQuality: 80, WebpQuality: 80, AvifQuality: 60,
		JxlQuality: 80, AvifEffort: 2, JxlEffort: 3, WebpEffort: 3, PngCompression: 6, FaceMaxSide: 640,
		AutoFormats: []string{"webp", "avif"}, FaceEngine: "yunet", FaceScore: 0.6, ORTLib: ortLib,
		FaceMinQuality: 30, FaceTilt: "fallback",
	}
	var err error
	if proc, err = New(cfg); err != nil {
		panic(err)
	}
	buildFixtures()
	code := m.Run()
	vips.Shutdown()
	os.Exit(code)
}

// photo génère une image de test (dégradés + formes) en PNG.
func photo(w, h int, alpha bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			a := uint8(255)
			if alpha && x < w/4 {
				a = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), uint8((x ^ y) & 255), a})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func animatedGIF() []byte {
	g := &gif.GIF{}
	for i := range 3 {
		fr := image.NewPaletted(image.Rect(0, 0, 160, 120), palette.Plan9)
		for y := range 120 {
			for x := range 160 {
				fr.SetColorIndex(x, y, uint8((x+y+i*40)%200))
			}
		}
		g.Image, g.Delay = append(g.Image, fr), append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func buildFixtures() {
	base := photo(640, 480, false)
	fixtures["png"] = base
	fixtures["png_alpha"] = photo(320, 240, true)
	fixtures["gif_anim"] = animatedGIF()
	fixtures["svg"] = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="400" height="300"><rect width="400" height="300" fill="#36c"/><circle cx="200" cy="150" r="90" fill="#fc3"/></svg>`)

	exp := func(name string, f func(*vips.ImageRef) ([]byte, *vips.ImageMetadata, error)) {
		img, err := vips.NewImageFromBuffer(base)
		if err != nil {
			panic(err)
		}
		defer img.Close()
		if b, _, err := f(img); err == nil {
			fixtures[name] = b
		} else {
			fixtures[name] = nil // encodeur absent : signalé par le test
		}
	}
	exp("jpeg", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportJpeg(vips.NewJpegExportParams())
	})
	exp("webp", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportWebp(vips.NewWebpExportParams())
	})
	exp("avif", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportAvif(vips.NewAvifExportParams())
	})
	exp("jxl", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportJxl(vips.NewJxlExportParams())
	})
	exp("tiff", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportTiff(vips.NewTiffExportParams())
	})
	exp("gif", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportGIF(vips.NewGifExportParams())
	})
	exp("bmp", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportMagick(&vips.MagickExportParams{Format: "bmp"})
	})
	exp("psd", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		return i.ExportMagick(&vips.MagickExportParams{Format: "psd"})
	})
	exp("ico", func(i *vips.ImageRef) ([]byte, *vips.ImageMetadata, error) {
		_ = i.Thumbnail(128, 128, vips.InterestingNone)
		return i.ExportMagick(&vips.MagickExportParams{Format: "ico"})
	})
}

func run(t *testing.T, src []byte, path, accept string) *Result {
	t.Helper()
	o, _, err := options.Parse(path+"/plain/local:///x", options.Limits{MaxDim: 8192})
	if err != nil {
		t.Fatalf("options %s : %v", path, err)
	}
	res, err := proc.Process(src, o, accept)
	if err != nil {
		t.Fatalf("%s : %v", path, err)
	}
	// La sortie doit être une image valide, du bon format.
	img, err := vips.NewImageFromBuffer(res.Body)
	if err != nil {
		t.Fatalf("%s : sortie illisible : %v", path, err)
	}
	img.Close()
	return res
}

// Tous les formats d'entrée doivent être lus et convertis.
func TestAllInputFormats(t *testing.T) {
	for _, name := range []string{"jpeg", "png", "png_alpha", "webp", "avif", "jxl", "tiff", "gif", "gif_anim", "svg", "bmp", "psd", "ico"} {
		t.Run(name, func(t *testing.T) {
			src := fixtures[name]
			if src == nil {
				t.Fatalf("encodeur %s indisponible dans libvips", name)
			}
			res := run(t, src, "/rs:fit:200:200", "")
			if res.Width > 200 || res.Height > 200 || res.Width == 0 {
				t.Errorf("dimensions %dx%d", res.Width, res.Height)
			}
			t.Logf("%-9s → %-4s %dx%d %6d octets", name, res.Format, res.Width, res.Height, len(res.Body))
		})
	}
}

// Tous les formats de sortie doivent être produits.
func TestAllOutputFormats(t *testing.T) {
	for _, f := range []string{"jpeg", "png", "webp", "avif", "jxl", "gif", "tiff"} {
		t.Run(f, func(t *testing.T) {
			res := run(t, fixtures["png_alpha"], "/w:100/f:"+f, "")
			if res.Format != f {
				t.Errorf("format %s au lieu de %s", res.Format, f)
			}
		})
	}
}

func TestOperations(t *testing.T) {
	src := fixtures["jpeg"]
	cases := []struct {
		path string
		w, h int
	}{
		{"/rs:fill:300:300", 300, 300},
		{"/rs:fill:300:150/g:sm", 300, 150},
		{"/rs:fill:300:150/g:face", 300, 150}, // sans visage → repli sur « attention »
		{"/rs:fill:300:150/g:soea", 300, 150},
		{"/rs:fill:200:200/g:fp:0.1:0.9", 200, 200},
		{"/rs:force:123:77", 123, 77},
		{"/c:320:240:nowe/rs:fit:160:0", 160, 120},
		{"/w:200/bl:3", 200, 150},
		{"/w:200/sh:1.5", 200, 150},
		{"/w:200/pix:10", 200, 150},
		{"/w:200/bf:10", 200, 150},
		{"/w:200/pf:12", 200, 150},
		{"/w:200/rot:90", 150, 200},
		{"/rs:auto:300:300", 300, 300},
		{"/rs:auto:300:200/g:sm", 300, 200},
		{"/rs:fill:300:150/g:face/bf:8", 300, 150},
		{"/w:1000/el:1", 1000, 750},
		{"/w:1000", 640, 480}, // pas d'agrandissement par défaut
	}
	for _, c := range cases {
		res := run(t, src, c.path, "")
		if res.Width != c.w || res.Height != c.h {
			t.Errorf("%s : %dx%d au lieu de %dx%d", c.path, res.Width, res.Height, c.w, c.h)
		}
	}
}

func TestAutoFormat(t *testing.T) {
	if r := run(t, fixtures["jpeg"], "/w:100/f:auto", "image/avif,image/webp,*/*"); r.Format != "webp" {
		t.Errorf("auto → %s (webp attendu)", r.Format)
	}
	if r := run(t, fixtures["jpeg"], "/w:100/f:auto", "image/avif,*/*"); r.Format != "avif" {
		t.Errorf("auto → %s (avif attendu)", r.Format)
	}
	if r := run(t, fixtures["png_alpha"], "/w:100/f:auto", "*/*"); r.Format != "png" {
		t.Errorf("auto sans support + alpha → %s (png attendu)", r.Format)
	}
}

func TestAnimationKept(t *testing.T) {
	r := run(t, fixtures["gif_anim"], "/rs:fit:80:80", "")
	if !r.Animated || r.Format != "gif" || r.Width != 80 || r.Height != 60 {
		t.Errorf("animation : %+v", r)
	}
	r = run(t, fixtures["gif_anim"], "/rs:fit:80:80/f:webp", "")
	if !r.Animated {
		t.Error("animation perdue en webp")
	}
	if r = run(t, fixtures["gif_anim"], "/rs:fit:80:80/f:jpeg", ""); r.Animated {
		t.Error("jpeg ne peut pas être animé")
	}
}

func TestLimits(t *testing.T) {
	small := *proc.cfg
	small.MaxSrcPixels = 1000
	p := &Processor{cfg: &small, faces: proc.faces}
	o, _, _ := options.Parse("/w:10/plain/local:///x", options.Limits{})
	if _, err := p.Process(fixtures["jpeg"], o, ""); !errors.Is(err, ErrTooManyPixels) {
		t.Errorf("limite de pixels non appliquée : %v", err)
	}
	if _, err := proc.Process([]byte("ceci n'est pas une image"), o, ""); !errors.Is(err, ErrUnsupported) {
		t.Errorf("fichier invalide : %v", err)
	}
	if _, err := proc.Process(fixtures["jpeg"][:200], o, ""); err == nil {
		t.Error("fichier tronqué accepté")
	}
}

func TestSourceFormat(t *testing.T) {
	for name, want := range map[string]string{"jpeg": "jpeg", "png": "png", "webp": "webp", "svg": "svg", "gif": "gif"} {
		if got := SourceFormat(fixtures[name]); !strings.EqualFold(got, want) {
			t.Errorf("%s détecté comme %s", name, got)
		}
	}
}

func testdata(t testing.TB, name string) []byte {
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Vrais visages (photos NASA, domaine public) : comptage exact, pas de faux positifs.
func TestFaceDetectionReal(t *testing.T) {
	for name, want := range map[string]int{"portrait-noguchi.jpg": 1, "groupe-equipage.jpg": 7} {
		if r := run(t, testdata(t, name), "/w:800/fd", ""); r.Faces != want {
			t.Errorf("%s : %d visages détectés, %d attendus", name, r.Faces, want)
		}
	}
}

// Les centrages doivent produire des cadrages différents (bug signalé : haut = bas = visage).
func TestGravityChangesFraming(t *testing.T) {
	// 1280×1024 → 400×200 : seule la hauteur est recadrée, on compare donc les centrages verticaux.
	src := testdata(t, "groupe-equipage.jpg")
	seen := map[string]string{}
	for _, g := range []string{"ce", "no", "so", "face"} {
		r := run(t, src, "/rs:fill:400:200/g:"+g+"/f:png", "")
		if r.Width != 400 || r.Height != 200 {
			t.Errorf("g:%s : %dx%d", g, r.Width, r.Height)
		}
		if prev, ok := seen[string(r.Body)]; ok {
			t.Errorf("g:%s donne la même image que g:%s", g, prev)
		}
		seen[string(r.Body)] = g
	}
}

func TestFaceZoom(t *testing.T) {
	portrait := testdata(t, "portrait-noguchi.jpg")
	r := run(t, portrait, "/rs:fill:256:256/fc:0.4", "")
	if r.Width != 256 || r.Height != 256 || r.Faces != 1 {
		t.Errorf("avatar : %dx%d, %d visage(s)", r.Width, r.Height, r.Faces)
	}
	// Le visage doit être au centre de l'avatar : on le redétecte dans le résultat.
	r2 := run(t, r.Body, "/fd", "")
	if r2.Faces != 1 {
		t.Errorf("visage perdu après zoom (%d)", r2.Faces)
	}
	g := run(t, testdata(t, "groupe-equipage.jpg"), "/rs:fill:300:400/fc:0.3:largest", "")
	if g.Width != 300 || g.Height != 400 || g.Faces != 7 {
		t.Errorf("visage principal : %dx%d, %d détectés", g.Width, g.Height, g.Faces)
	}
	// Sans visage : repli sur un recadrage « smart » aux dimensions demandées.
	if n := run(t, fixtures["jpeg"], "/rs:fill:200:200/fc:0.5", ""); n.Width != 200 || n.Height != 200 || n.Faces != 0 {
		t.Errorf("sans visage : %dx%d, %d", n.Width, n.Height, n.Faces)
	}
}

// Groupe trop large pour le cadre : g:face doit centrer sur le visage principal,
// donc donner un cadrage nettement différent du centre.
func TestFaceGravityGroup(t *testing.T) {
	src := testdata(t, "groupe-equipage.jpg")
	ce := run(t, src, "/rs:fill:300:300/f:png", "")
	fa := run(t, src, "/rs:fill:300:300/g:face/f:png", "")
	if string(ce.Body) == string(fa.Body) || fa.Faces != 7 {
		t.Errorf("g:face identique au centre (visages : %d)", fa.Faces)
	}
	// Le résultat doit contenir au moins un visage entier.
	if r := run(t, fa.Body, "/fd", ""); r.Faces < 1 {
		t.Error("aucun visage dans le cadrage « visage »")
	}
}

// Floutage adouci : visages anonymisés, pas de canal alpha parasite.
func TestBlurFacesSoft(t *testing.T) {
	r := run(t, testdata(t, "groupe-equipage.jpg"), "/w:800/bf:12/f:png", "")
	img, err := vips.NewImageFromBuffer(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer img.Close()
	if img.HasAlpha() {
		t.Error("canal alpha ajouté par le fondu")
	}
	if again := run(t, r.Body, "/fd", ""); again.Faces > 1 {
		t.Errorf("%d visages encore reconnaissables après floutage", again.Faces)
	}
	if r := run(t, testdata(t, "groupe-equipage.jpg"), "/w:800/pf:14/f:jpeg", ""); r.Faces != 7 {
		t.Errorf("pixellisation : %d visages traités", r.Faces)
	}
}

// Fondu elliptique sur TOUS les visages : les coins de chaque zone floutée doivent rester
// identiques à l'original (un collage rectangulaire les modifierait).
func TestBlurFacesNoRectangle(t *testing.T) {
	src := testdata(t, "groupe-equipage.jpg")
	raw := func(path string) ([]byte, *vips.ImageRef) {
		r := run(t, src, path+"/f:png", "")
		img, err := vips.NewImageFromBuffer(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := img.ToBytes()
		return b, img
	}
	orig, oi := raw("/w:800")
	defer oi.Close()
	blur, bi := raw("/w:800/bf:12")
	defer bi.Close()
	w, bands := oi.Width(), oi.Bands()
	faces := proc.detectFaces(oi)
	if len(faces) != 7 {
		t.Fatalf("%d visages", len(faces))
	}
	for i, f := range faces {
		hr := HeadRegion(f, oi.Width(), oi.Height())
		for _, c := range [][2]int{{hr.X + 1, hr.Y + 1}, {hr.X + hr.W - 2, hr.Y + 1}, {hr.X + 1, hr.Y + hr.H - 2}, {hr.X + hr.W - 2, hr.Y + hr.H - 2}} {
			o := (c[1]*w + c[0]) * bands
			for k := range bands {
				if d := int(orig[o+k]) - int(blur[o+k]); d > 10 || d < -10 {
					t.Errorf("visage %d : coin (%d,%d) modifié (écart %d) → bord rectangulaire", i, c[0], c[1], d)
					break
				}
			}
		}
	}
}

// rotateJPEG incline une image de deg degrés (bilinéaire, fond gris) et la renvoie en PNG.
func rotateJPEG(t *testing.T, src []byte, deg float64) []byte {
	img, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	sin, cos := math.Sincos(deg * math.Pi / 180)
	cx, cy := float64(w)/2, float64(h)/2
	for y := range h {
		for x := range w {
			// Pixel source correspondant (rotation inverse).
			sx := cos*(float64(x)-cx) + sin*(float64(y)-cy) + cx
			sy := -sin*(float64(x)-cx) + cos*(float64(y)-cy) + cy
			if sx < 0 || sy < 0 || sx >= float64(w-1) || sy >= float64(h-1) {
				out.Set(x, y, color.RGBA{128, 128, 128, 255})
				continue
			}
			x0, y0 := int(sx), int(sy)
			fx, fy := sx-float64(x0), sy-float64(y0)
			var c [3]float64
			for _, p := range []struct {
				dx, dy int
				wt     float64
			}{{0, 0, (1 - fx) * (1 - fy)}, {1, 0, fx * (1 - fy)}, {0, 1, (1 - fx) * fy}, {1, 1, fx * fy}} {
				r, g, bb, _ := img.At(b.Min.X+x0+p.dx, b.Min.Y+y0+p.dy).RGBA()
				c[0] += float64(r>>8) * p.wt
				c[1] += float64(g>>8) * p.wt
				c[2] += float64(bb>>8) * p.wt
			}
			out.Set(x, y, color.RGBA{uint8(c[0]), uint8(c[1]), uint8(c[2]), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Le moteur principal doit être YuNet : si ONNX Runtime manque, le service se rabat
// silencieusement sur pigo — la CI doit le voir.
func TestEngineIsYuNet(t *testing.T) {
	if e := proc.FaceEngine(); e != "yunet" {
		t.Fatalf("moteur de visages %q au lieu de yunet (ONNX Runtime absent ?)", e)
	}
}

// pigo (secours) : têtes inclinées. Mesuré sur ce portrait : sans gestion de l'inclinaison,
// rien au-delà de ~20° ; avec, -25° et -30° sont détectés.
func TestPigoTiltedFace(t *testing.T) {
	pigo := func(tilt string) *Processor {
		cfg := *proc.cfg
		cfg.FaceTilt = tilt
		d, err := NewPigo(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		return &Processor{cfg: &cfg, faces: d}
	}
	off, fallback := pigo("off"), pigo("fallback")
	o, _, _ := options.Parse("/w:800/fd/plain/local:///x", options.Limits{MaxDim: 8192})
	for _, deg := range []float64{-25, -30} {
		tilted := rotateJPEG(t, testdata(t, "portrait-noguchi.jpg"), deg)
		before, err1 := off.Process(tilted, o, "")
		after, err2 := fallback.Process(tilted, o, "")
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if before.Faces != 0 || after.Faces != 1 {
			t.Errorf("pigo %v° : %d visage(s) sans inclinaison (0 attendu), %d avec (1 attendu)", deg, before.Faces, after.Faces)
		}
	}
}

// YuNet : têtes inclinées dans les deux sens, sans réglage particulier.
func TestYuNetTiltedFace(t *testing.T) {
	for _, deg := range []float64{-30, -20, 20, 30} {
		r := run(t, rotateJPEG(t, testdata(t, "portrait-noguchi.jpg"), deg), "/w:800/fd", "")
		if r.Faces != 1 {
			t.Errorf("tête inclinée de %v° : %d visage(s), 1 attendu", deg, r.Faces)
		}
	}
}

// YuNet : petits visages (cas réel signalé : groupe en pied, visages de ~20 px).
func TestYuNetSmallFaces(t *testing.T) {
	small := run(t, testdata(t, "groupe-equipage.jpg"), "/w:420/f:png", "") // visages ≈ 30 px
	if r := run(t, small.Body, "/fd", ""); r.Faces != 7 {
		t.Errorf("petits visages : %d détectés, 7 attendus", r.Faces)
	}
}

func TestEllipseMask(t *testing.T) {
	m := EllipseMask(100, 120, 0.6)
	at := func(x, y int) byte { return m[y*100+x] }
	if at(50, 60) != 255 {
		t.Errorf("centre = %d, attendu opaque", at(50, 60))
	}
	if at(0, 0) != 0 || at(99, 119) != 0 {
		t.Error("coins non transparents")
	}
	if v := at(50, 3); v == 0 || v == 255 {
		t.Errorf("bord haut = %d, attendu un fondu partiel", v)
	}
}

func TestCropFraction(t *testing.T) {
	if r := run(t, fixtures["jpeg"], "/c:0.5:0.5:soea", ""); r.Width != 320 || r.Height != 240 {
		t.Errorf("c:0.5:0.5 → %dx%d", r.Width, r.Height)
	}
}

func TestFaceRegion(t *testing.T) {
	// Visage 100×100 au centre d'une image 1000×1000, marge 0.5, cible carrée.
	x, y, w, h := FaceRegion(face.Rect{X: 450, Y: 450, W: 100, H: 100}, 0.5, 1000, 1000, 256, 256)
	if w != 200 || h != 200 || x != 400 || y != 410 {
		t.Errorf("zone %d,%d %dx%d", x, y, w, h)
	}
	// Cible portrait 3:4 : la zone s'allonge.
	_, _, w, h = FaceRegion(face.Rect{X: 450, Y: 450, W: 100, H: 100}, 0.5, 1000, 1000, 300, 400)
	if w != 200 || h != 267 {
		t.Errorf("3:4 : %dx%d", w, h)
	}
	// Visage au bord : la zone reste dans l'image.
	x, y, w, h = FaceRegion(face.Rect{X: 0, Y: 0, W: 100, H: 100}, 1, 300, 300, 0, 0)
	if x != 0 || y != 0 || w != 300 || h != 300 {
		t.Errorf("bord : %d,%d %dx%d", x, y, w, h)
	}
}

func BenchmarkJPEG4KFit800(b *testing.B) {
	img, _ := vips.NewImageFromBuffer(photo(4000, 3000, false))
	src, _, _ := img.ExportJpeg(vips.NewJpegExportParams())
	img.Close()
	o, _, _ := options.Parse("/rs:fit:800:800/plain/local:///x", options.Limits{MaxDim: 8192})
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := proc.Process(src, o, ""); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkJPEG4KFaceWebp(b *testing.B) {
	img, _ := vips.NewImageFromBuffer(photo(4000, 3000, false))
	src, _, _ := img.ExportJpeg(vips.NewJpegExportParams())
	img.Close()
	o, _, _ := options.Parse("/rs:fill:600:400/g:face/f:webp/plain/local:///x", options.Limits{MaxDim: 8192})
	for b.Loop() {
		if _, err := proc.Process(src, o, ""); err != nil {
			b.Fatal(err)
		}
	}
}
