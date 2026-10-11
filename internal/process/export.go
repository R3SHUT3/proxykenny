package process

import (
	"fmt"
	"proxykenny/internal/options"
	"strings"

	"github.com/davidbyttow/govips/v2/vips"
)

var contentTypes = map[string]string{
	"jpeg": "image/jpeg", "png": "image/png", "webp": "image/webp", "avif": "image/avif",
	"jxl": "image/jxl", "gif": "image/gif", "tiff": "image/tiff",
}

// outFormat choisit le format de sortie.
func (p *Processor) outFormat(req, src string, alpha bool, accept string, animated bool) string {
	fallback := "jpeg"
	if alpha {
		fallback = "png"
	}
	if animated {
		fallback = "gif"
	}
	switch req {
	case "":
		switch src {
		case "jpeg", "png", "webp", "avif", "jxl", "gif", "tiff":
			return src
		}
		return fallback
	case "auto":
		for _, f := range p.cfg.AutoFormats {
			if animated && f != "webp" && f != "gif" {
				continue
			}
			if _, known := contentTypes[f]; known && strings.Contains(accept, "image/"+f) {
				return f
			}
		}
		return fallback
	}
	return req
}

func (p *Processor) export(img *vips.ImageRef, format string, o *options.Options) ([]byte, error) {
	if o.Strip {
		if err := img.RemoveMetadata(); err != nil {
			return nil, err
		}
	}
	if img.Interpretation() == vips.InterpretationCMYK {
		if err := img.ToColorSpace(vips.InterpretationSRGB); err != nil {
			return nil, err
		}
	}
	if format == "jpeg" && img.HasAlpha() {
		bg := vips.Color{R: 255, G: 255, B: 255}
		if o.Bg != nil {
			bg = vips.Color{R: o.Bg[0], G: o.Bg[1], B: o.Bg[2]}
		}
		if err := img.Flatten(&bg); err != nil {
			return nil, err
		}
	}
	q := func(def int) int {
		if o.Quality > 0 {
			return o.Quality
		}
		return def
	}
	var (
		buf []byte
		err error
	)
	switch format {
	case "jpeg":
		ep := vips.NewJpegExportParams()
		ep.Quality, ep.StripMetadata, ep.OptimizeCoding = q(p.cfg.JpegQuality), o.Strip, true
		buf, _, err = img.ExportJpeg(ep)
	case "png":
		ep := vips.NewPngExportParams()
		ep.StripMetadata, ep.Compression = o.Strip, p.cfg.PngCompression
		buf, _, err = img.ExportPng(ep)
	case "webp":
		ep := vips.NewWebpExportParams()
		ep.Quality, ep.StripMetadata, ep.ReductionEffort = q(p.cfg.WebpQuality), o.Strip, p.cfg.WebpEffort
		buf, _, err = img.ExportWebp(ep)
	case "avif":
		ep := vips.NewAvifExportParams()
		ep.Quality, ep.StripMetadata, ep.Effort, ep.Bitdepth = q(p.cfg.AvifQuality), o.Strip, p.cfg.AvifEffort, 8
		buf, _, err = img.ExportAvif(ep)
	case "jxl":
		ep := vips.NewJxlExportParams()
		ep.Quality, ep.Effort = q(p.cfg.JxlQuality), p.cfg.JxlEffort
		buf, _, err = img.ExportJxl(ep)
	case "gif":
		ep := vips.NewGifExportParams()
		ep.StripMetadata = o.Strip
		buf, _, err = img.ExportGIF(ep)
	case "tiff":
		ep := vips.NewTiffExportParams()
		ep.StripMetadata = o.Strip
		buf, _, err = img.ExportTiff(ep)
	default:
		return nil, fmt.Errorf("format de sortie %q non géré", format)
	}
	return buf, err
}
