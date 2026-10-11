// Package process transforme les images avec libvips.
//
// Principes de performance :
//   - « shrink-on-load » : pour un redimensionnement, l'image est réduite pendant le
//     décodage (JPEG, WebP, HEIF...) au lieu d'être décodée en pleine taille ;
//   - libvips travaille en flux, par tuiles : la mémoire reste faible même sur de grosses images ;
//   - la détection de visages (YuNet) se fait sur une petite copie de l'image (≤ 640 px).
//
// Organisation : process.go (pipeline), load.go (chargement), gravity.go (cadrage),
// faces.go (visages), effects.go (effets), export.go (encodage).
package process

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/davidbyttow/govips/v2/vips"

	"proxykenny/internal/config"
	"proxykenny/internal/face"
	"proxykenny/internal/options"
)

var (
	ErrUnsupported   = errors.New("format source non supporté")
	ErrTooManyPixels = errors.New("image source trop grande (pixels)")
	ErrProcessing    = errors.New("erreur de traitement")
)

type Processor struct {
	cfg   *config.Config
	faces face.Engine
}

// FaceEngine renvoie le nom du moteur de détection de visages actif.
func (p *Processor) FaceEngine() string { return p.faces.Name() }

type Result struct {
	Body          []byte
	Format        string // format de sortie (jpeg, webp...)
	ContentType   string
	SrcFormat     string
	Width, Height int
	Faces         int  // visages détectés (-1 si pas de détection)
	Animated      bool // sortie animée
}

func New(cfg *config.Config) (*Processor, error) {
	if cfg.FaceEngine != "pigo" {
		y, err := face.NewYuNet(cfg.ORTLib)
		if err == nil {
			if cfg.FaceScore > 0 {
				y.ScoreThreshold = float32(cfg.FaceScore)
			}
			return &Processor{cfg: cfg, faces: y}, nil
		}
		// Moteur principal indisponible (bibliothèque absente...) : on continue avec pigo.
		slog.Warn("YuNet indisponible, détection de visages avec pigo", "err", err)
	}
	d, err := NewPigo(cfg)
	if err != nil {
		return nil, err
	}
	return &Processor{cfg: cfg, faces: d}, nil
}

// NewPigo crée le détecteur pigo (secours) avec les réglages de la configuration.
func NewPigo(cfg *config.Config) (*face.Detector, error) {
	d, err := face.New()
	if err != nil {
		return nil, err
	}
	if cfg.FaceMinQuality > 0 {
		d.MinQuality = float32(cfg.FaceMinQuality)
	}
	switch t := face.TiltMode(cfg.FaceTilt); t {
	case face.TiltOff, face.TiltFallback, face.TiltAlways:
		d.Tilt = t
	}
	return d, nil
}

var srcNames = map[vips.ImageType]string{
	vips.ImageTypeJPEG: "jpeg", vips.ImageTypePNG: "png", vips.ImageTypeWEBP: "webp",
	vips.ImageTypeAVIF: "avif", vips.ImageTypeHEIF: "heif", vips.ImageTypeJXL: "jxl",
	vips.ImageTypeGIF: "gif", vips.ImageTypeTIFF: "tiff", vips.ImageTypeSVG: "svg",
	vips.ImageTypePDF: "pdf", vips.ImageTypeBMP: "bmp", vips.ImageTypeJP2K: "jp2k",
	vips.ImageTypePSD: "psd", vips.ImageTypeMagick: "magick",
}

// SourceFormat renvoie le nom du format détecté depuis les premiers octets.
func SourceFormat(src []byte) string {
	if n, ok := srcNames[vips.DetermineImageType(src)]; ok {
		return n
	}
	return "unknown"
}

// Process applique les options à l'image source.
// accept est l'en-tête HTTP Accept, utilisé pour f:auto.
func (p *Processor) Process(src []byte, o *options.Options, accept string) (*Result, error) {
	srcType := vips.DetermineImageType(src)
	srcName, ok := srcNames[srcType]
	if !ok {
		return nil, ErrUnsupported
	}
	res := &Result{SrcFormat: srcName, Faces: -1}

	// 1. Lecture de l'en-tête seulement (libvips est paresseux) : contrôle des limites
	//    avant tout décodage coûteux.
	w, h, pages, orient, err := header(src, o.Page)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if w*h > p.cfg.MaxSrcPixels {
		return nil, ErrTooManyPixels
	}
	if o.AutoRot && orient >= 5 && orient <= 8 {
		w, h = h, w // dimensions après rotation EXIF
	}

	faceOps := o.BlurFace > 0 || o.PixFace > 0 || o.Gravity.Type == options.GravityFace ||
		(o.Crop != nil && o.Crop.Gravity.Type == options.GravityFace) || o.FaceZoom > 0 || o.FaceDbg
	rt := o.Resize
	if rt == options.ResizeAuto && !(o.Width > 0 && o.Height > 0) {
		rt = options.ResizeFit
	}
	animated := pages > 1 && (srcType == vips.ImageTypeGIF || srcType == vips.ImageTypeWEBP) &&
		o.Crop == nil && rt != options.ResizeFill && rt != options.ResizeAuto && !faceOps && o.Rotate == 0 &&
		pages <= p.cfg.MaxFrames && w*h*pages <= p.cfg.MaxSrcPixels

	// 2. Chargement + redimensionnement.
	var img *vips.ImageRef
	zoomed := o.FaceZoom > 0 && o.Crop == nil
	if zoomed {
		img, err = p.loadFaceZoom(src, o, res)
	} else {
		img, err = p.load(src, o, w, h, animated)
		if err == nil && (faceOps || (o.Gravity.Type == options.GravitySmart && rt == options.ResizeAuto)) && o.Crop == nil {
			// Le chargement réduit est séquentiel : la détection lirait les pixels une
			// première fois, puis le recadrage une seconde → on matérialise l'image réduite.
			if img, err = materialize(img); err != nil {
				err = fmt.Errorf("%w: %v", ErrProcessing, err)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	defer func() { img.Close() }() // img peut être remplacée plus bas

	var cachedFaces []face.Rect
	detect := func() []face.Rect {
		if cachedFaces == nil {
			cachedFaces = p.detectFaces(img)
			res.Faces = len(cachedFaces)
		}
		return cachedFaces
	}

	// 3. Recadrage « fill » selon la gravité.
	if _, _, bw, bh, ok := Scale(rt, w, h, o.Width, o.Height, o.Enlarge); ok && o.Crop == nil && !zoomed &&
		(rt == options.ResizeFill || rt == options.ResizeAuto) {
		if err := p.gravityCrop(img, bw, bh, o.Gravity, detect); err != nil {
			return nil, fmt.Errorf("%w: crop: %v", ErrProcessing, err)
		}
	}

	// 4. Anonymisation des visages, effets.
	if o.BlurFace > 0 || o.PixFace > 0 {
		cachedFaces = nil // l'image a pu être redimensionnée depuis
		for _, r := range detect() {
			if err := p.anonymize(img, HeadRegion(r, img.Width(), img.Height()), o); err != nil {
				return nil, fmt.Errorf("%w: visages: %v", ErrProcessing, err)
			}
		}
	}
	if o.Blur > 0 {
		if err := img.GaussianBlur(o.Blur); err != nil {
			return nil, fmt.Errorf("%w: blur: %v", ErrProcessing, err)
		}
	}
	if o.Sharpen > 0 {
		if err := img.Sharpen(o.Sharpen, 2, 10); err != nil {
			return nil, fmt.Errorf("%w: sharpen: %v", ErrProcessing, err)
		}
	}
	if o.Pixelate > 1 {
		if err := pixelate(img, o.Pixelate); err != nil {
			return nil, fmt.Errorf("%w: pixelate: %v", ErrProcessing, err)
		}
	}
	if o.Rotate != 0 {
		angle := map[int]vips.Angle{90: vips.Angle90, 180: vips.Angle180, 270: vips.Angle270}[o.Rotate]
		if err := img.Rotate(angle); err != nil {
			return nil, fmt.Errorf("%w: rotate: %v", ErrProcessing, err)
		}
	}

	// Cadres autour des visages détectés (démonstration / contrôle de la détection).
	if o.FaceDbg {
		cachedFaces = nil
		faces := detect()
		if img, err = materialize(img); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProcessing, err)
		}
		if err := drawFaces(img, faces); err != nil {
			return nil, fmt.Errorf("%w: cadres: %v", ErrProcessing, err)
		}
	}

	// 5. Encodage.
	out := p.outFormat(o.Format, srcName, img.HasAlpha(), accept, animated)
	if animated && out != "gif" && out != "webp" {
		// Format sans animation : on garde la première image.
		if err := img.ExtractArea(0, 0, img.Width(), img.PageHeight()); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProcessing, err)
		}
		animated = false
	}
	body, err := p.export(img, out, o)
	if err != nil {
		return nil, fmt.Errorf("%w: encodage %s: %v", ErrProcessing, out, err)
	}
	res.Body, res.Format, res.ContentType, res.Animated = body, out, contentTypes[out], animated
	res.Width, res.Height = img.Width(), img.Height()
	if animated {
		res.Height = img.PageHeight()
	}
	return res, nil
}
