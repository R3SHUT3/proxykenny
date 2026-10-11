// Package options lit les URL de traitement, au format inspiré d'imgproxy :
//
//	/<signature>/<option>/<option>/.../plain/<url source>[@<format>]
//	/<signature>/<option>/<option>/.../<url source en base64url>[.<format>]
//
// Chaque option s'écrit nom:arg1:arg2 (ex. rs:fill:300:200, g:face, bl:5).
package options

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

type ResizeType string

const (
	ResizeFit   ResizeType = "fit"   // tient dans la boîte, garde les proportions
	ResizeFill  ResizeType = "fill"  // remplit la boîte, recadre ce qui dépasse
	ResizeForce ResizeType = "force" // déforme aux dimensions exactes
	ResizeAuto  ResizeType = "auto"  // fill si même orientation, sinon fit
)

type GravityType string

const (
	GravityCenter    GravityType = "ce"
	GravityNorth     GravityType = "no"
	GravitySouth     GravityType = "so"
	GravityEast      GravityType = "ea"
	GravityWest      GravityType = "we"
	GravityNorthEast GravityType = "noea"
	GravityNorthWest GravityType = "nowe"
	GravitySouthEast GravityType = "soea"
	GravitySouthWest GravityType = "sowe"
	GravitySmart     GravityType = "sm"   // zone la plus « intéressante » (libvips attention)
	GravityFace      GravityType = "face" // centrée sur les visages détectés
	GravityFocus     GravityType = "fp"   // point focal x:y (0..1)
)

type Gravity struct {
	Type   GravityType
	X, Y   float64 // fp : point focal relatif ; autres : décalage en pixels
	Offset bool
}

// Crop : dimensions en pixels, ou en proportion de l'image si ≤ 1 (ex. 0.5 = la moitié).
type Crop struct {
	W, H    float64
	Gravity Gravity
}

// Size renvoie les dimensions du recadrage pour une image w×h (0 = toute la dimension).
func (c *Crop) Size(w, h int) (int, int) {
	conv := func(v float64, full int) int {
		switch {
		case v <= 0:
			return full
		case v <= 1:
			return max(1, int(v*float64(full)+0.5))
		}
		return min(int(v), full)
	}
	return conv(c.W, w), conv(c.H, h)
}

type Options struct {
	Resize   ResizeType
	Width    int
	Height   int
	Enlarge  bool
	DPR      float64
	Gravity  Gravity
	Crop     *Crop
	Blur     float64 // sigma du flou gaussien
	Sharpen  float64 // sigma de la netteté
	Pixelate int     // taille des blocs
	BlurFace float64 // anonymisation : flou des visages
	PixFace  int     // anonymisation : pixellisation des visages
	FaceZoom float64 // > 0 : recadre serré autour des visages, marge relative (0.5 = +50 % de chaque côté)
	FaceOne  bool    // avec FaceZoom : seulement le visage principal (le plus grand)
	FaceDbg  bool    // dessine un cadre autour des visages détectés
	Rotate   int     // 0, 90, 180, 270
	Quality  int     // 0 = défaut du format
	Format   string  // jpeg, png, webp, avif, jxl, gif, tiff, auto ; "" = même que la source
	AutoRot  bool    // applique l'orientation EXIF
	Strip    bool    // supprime les métadonnées
	Page     int     // page (PDF, TIFF multipage)
	Bg       *[3]uint8
}

var (
	ErrNoSource = errors.New("source manquante")
)

// Limits borne les valeurs acceptées pour protéger la machine.
type Limits struct {
	MaxDim int
}

var outFormats = map[string]string{
	"jpg": "jpeg", "jpeg": "jpeg", "png": "png", "webp": "webp", "avif": "avif",
	"jxl": "jxl", "gif": "gif", "tiff": "tiff", "tif": "tiff", "auto": "auto", "best": "auto",
}

// Options utilisables sans argument.
var flagOptions = map[string]bool{"fd": true, "face_debug": true, "fc": true, "face_crop": true}

// NormalizeFormat renvoie le nom canonique d'un format de sortie, ou "".
func NormalizeFormat(f string) string { return outFormats[strings.ToLower(f)] }

func defaults() *Options {
	return &Options{Resize: ResizeFit, DPR: 1, Gravity: Gravity{Type: GravityCenter}, AutoRot: true, Strip: true}
}

// Parse lit le chemin (sans la signature) et renvoie les options et l'URL source.
func Parse(path string, lim Limits) (*Options, string, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	o := defaults()
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "plain" {
			src, err := url.PathUnescape(strings.Join(parts[i+1:], "/"))
			if err != nil {
				return nil, "", fmt.Errorf("source plain invalide: %w", err)
			}
			if at := strings.LastIndex(src, "@"); at > strings.Index(src, "://")+2 && at >= 0 {
				if f := NormalizeFormat(src[at+1:]); f != "" {
					o.Format = f
					src = src[:at]
				}
			}
			if src == "" {
				return nil, "", ErrNoSource
			}
			return o, src, validate(o, lim)
		}
		if flagOptions[p] {
			// Option sans argument (ex. « fd », « fc »).
			if err := apply(o, p); err != nil {
				return nil, "", err
			}
			continue
		}
		if !strings.Contains(p, ":") {
			// Dernier segment : source encodée en base64url, extension optionnelle.
			if i != len(parts)-1 {
				return nil, "", fmt.Errorf("option inconnue %q", p)
			}
			enc := p
			if dot := strings.LastIndex(p, "."); dot > 0 {
				if f := NormalizeFormat(p[dot+1:]); f != "" {
					o.Format = f
					enc = p[:dot]
				}
			}
			b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(enc, "="))
			if err != nil || len(b) == 0 {
				return nil, "", fmt.Errorf("source base64 invalide")
			}
			return o, string(b), validate(o, lim)
		}
		if err := apply(o, p); err != nil {
			return nil, "", err
		}
	}
	return nil, "", ErrNoSource
}

func apply(o *Options, seg string) error {
	a := strings.Split(seg, ":")
	name, args := a[0], a[1:]
	var err error
	switch name {
	case "resize", "rs":
		if len(args) > 0 && args[0] != "" {
			if o.Resize, err = resizeType(args[0]); err != nil {
				return err
			}
		}
		err = dims(o, args[min(1, len(args)):])
	case "size", "s":
		err = dims(o, args)
	case "resizing_type", "rt":
		o.Resize, err = resizeType(arg(args, 0))
	case "width", "w":
		o.Width, err = atoi(arg(args, 0))
	case "height", "h":
		o.Height, err = atoi(arg(args, 0))
	case "enlarge", "el":
		o.Enlarge = boolArg(arg(args, 0))
	case "dpr":
		o.DPR, err = atof(arg(args, 0))
	case "gravity", "g":
		o.Gravity, err = gravity(args)
	case "crop", "c":
		c := &Crop{Gravity: Gravity{Type: GravityCenter}}
		if c.W, err = atof(arg(args, 0)); err != nil {
			return err
		}
		if c.H, err = atof(arg(args, 1)); err != nil {
			return err
		}
		if len(args) > 2 {
			if c.Gravity, err = gravity(args[2:]); err != nil {
				return err
			}
		}
		o.Crop = c
	case "blur", "bl":
		o.Blur, err = atof(arg(args, 0))
	case "sharpen", "sh":
		o.Sharpen, err = atof(arg(args, 0))
	case "pixelate", "pix":
		o.Pixelate, err = atoi(arg(args, 0))
	case "blur_faces", "bf":
		o.BlurFace, err = atof(arg(args, 0))
	case "pixelate_faces", "pf":
		o.PixFace, err = atoi(arg(args, 0))
	case "face_crop", "fc":
		o.FaceZoom = 0.5
		if v := arg(args, 0); v != "" {
			o.FaceZoom, err = atof(v)
		}
		o.FaceOne = arg(args, 1) == "largest" || arg(args, 1) == "1"
	case "face_debug", "fd":
		o.FaceDbg = len(args) == 0 || boolArg(arg(args, 0))
	case "rotate", "rot":
		o.Rotate, err = atoi(arg(args, 0))
	case "quality", "q":
		o.Quality, err = atoi(arg(args, 0))
	case "format", "f", "ext":
		if o.Format = NormalizeFormat(arg(args, 0)); o.Format == "" {
			return fmt.Errorf("format de sortie inconnu %q", arg(args, 0))
		}
	case "auto_rotate", "ar":
		o.AutoRot = boolArg(arg(args, 0))
	case "strip_metadata", "sm":
		o.Strip = boolArg(arg(args, 0))
	case "page", "pg":
		o.Page, err = atoi(arg(args, 0))
	case "background", "bg":
		o.Bg, err = color(arg(args, 0))
	default:
		return fmt.Errorf("option inconnue %q", name)
	}
	if err != nil {
		return fmt.Errorf("option %s: %w", name, err)
	}
	return nil
}

func validate(o *Options, lim Limits) error {
	if o.DPR <= 0 || o.DPR > 4 {
		return fmt.Errorf("dpr doit être entre 0 et 4")
	}
	if o.DPR != 1 {
		o.Width = int(float64(o.Width)*o.DPR + 0.5)
		o.Height = int(float64(o.Height)*o.DPR + 0.5)
		o.DPR = 1
	}
	max := lim.MaxDim
	if max <= 0 {
		max = 8192
	}
	for _, v := range []int{o.Width, o.Height} {
		if v < 0 || v > max {
			return fmt.Errorf("dimension hors limites (0..%d)", max)
		}
	}
	if o.Crop != nil && (o.Crop.W < 0 || o.Crop.H < 0 || o.Crop.W > 65535 || o.Crop.H > 65535) {
		return fmt.Errorf("crop hors limites")
	}
	if o.FaceZoom < 0 || o.FaceZoom > 5 {
		return fmt.Errorf("fc : marge entre 0 et 5")
	}
	if o.Blur < 0 || o.Blur > 100 || o.Sharpen < 0 || o.Sharpen > 10 || o.BlurFace < 0 || o.BlurFace > 100 {
		return fmt.Errorf("sigma hors limites")
	}
	if o.Pixelate < 0 || o.Pixelate > 512 || o.PixFace < 0 || o.PixFace > 512 {
		return fmt.Errorf("pixelate hors limites")
	}
	if o.Quality < 0 || o.Quality > 100 {
		return fmt.Errorf("qualité hors limites (1..100)")
	}
	switch ((o.Rotate % 360) + 360) % 360 {
	case 0, 90, 180, 270:
		o.Rotate = ((o.Rotate % 360) + 360) % 360
	default:
		return fmt.Errorf("rotation : multiples de 90 uniquement")
	}
	if o.Page < 0 || o.Page > 10000 {
		return fmt.Errorf("page hors limites")
	}
	return nil
}

func dims(o *Options, a []string) error {
	var err error
	if v := arg(a, 0); v != "" {
		if o.Width, err = atoi(v); err != nil {
			return err
		}
	}
	if v := arg(a, 1); v != "" {
		if o.Height, err = atoi(v); err != nil {
			return err
		}
	}
	if v := arg(a, 2); v != "" {
		o.Enlarge = boolArg(v)
	}
	return nil
}

func resizeType(s string) (ResizeType, error) {
	switch t := ResizeType(s); t {
	case ResizeFit, ResizeFill, ResizeForce, ResizeAuto:
		return t, nil
	}
	return "", fmt.Errorf("type de redimensionnement inconnu %q", s)
}

func gravity(a []string) (Gravity, error) {
	g := Gravity{Type: GravityType(arg(a, 0))}
	switch g.Type {
	case GravityCenter, GravityNorth, GravitySouth, GravityEast, GravityWest,
		GravityNorthEast, GravityNorthWest, GravitySouthEast, GravitySouthWest:
		if len(a) >= 3 {
			x, err1 := atof(a[1])
			y, err2 := atof(a[2])
			if err1 != nil || err2 != nil {
				return g, fmt.Errorf("décalage de gravité invalide")
			}
			g.X, g.Y, g.Offset = x, y, true
		}
	case GravitySmart, GravityFace:
	case GravityFocus:
		x, err1 := atof(arg(a, 1))
		y, err2 := atof(arg(a, 2))
		if err1 != nil || err2 != nil || x < 0 || x > 1 || y < 0 || y > 1 {
			return g, fmt.Errorf("point focal : fp:x:y avec x,y entre 0 et 1")
		}
		g.X, g.Y = x, y
	default:
		return g, fmt.Errorf("gravité inconnue %q", g.Type)
	}
	return g, nil
}

func color(s string) (*[3]uint8, error) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return nil, fmt.Errorf("couleur hex attendue (rrggbb)")
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return nil, fmt.Errorf("couleur hex invalide")
	}
	return &[3]uint8{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
}

func arg(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

func atoi(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("entier attendu, reçu %q", s)
	}
	return v, nil
}

func atof(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("nombre attendu, reçu %q", s)
	}
	return v, nil
}

func boolArg(s string) bool { return s == "1" || s == "t" || s == "true" }
