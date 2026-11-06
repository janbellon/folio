package pdfops

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"

	"github.com/disintegration/imaging"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// ImageOptions controls page layout. PageSize is A4, Letter or fit, the latter
// giving each image a page of its own dimensions.
type ImageOptions struct {
	PageSize  string
	Landscape bool
	Margin    bool
	MaxPixels int64
}

// checkImage rejects unknown formats and decompression bombs from the header
// alone, before anything allocates the full bitmap.
func checkImage(name string, data []byte, maxPixels int64) (format string, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", inputErrorf("%s is not a supported image, use PNG, JPEG, WebP or TIFF", name)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return "", inputErrorf("%s is too large, the limit is %d megapixels", name, maxPixels/1_000_000)
	}
	return format, nil
}

// jpegOrientation returns the EXIF orientation tag, or 1 when there is none.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		marker := b[i+1]
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return exifOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int64(order.Uint32(t[4:]))
	if ifd+2 > int64(len(t)) {
		return 1
	}
	count := int64(order.Uint16(t[ifd:]))
	for n := int64(0); n < count; n++ {
		e := ifd + 2 + n*12
		if e+12 > int64(len(t)) {
			return 1
		}
		if order.Uint16(t[e:]) == 0x0112 {
			if v := int(order.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// normalize bakes the EXIF rotation into the pixels since pdfcpu ignores the tag.
// Upright JPEGs are passed through so they are embedded without recompression.
func normalize(data []byte, format string) ([]byte, error) {
	if format != "jpeg" || jpegOrientation(data) == 1 {
		return data, nil
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := imaging.Encode(&out, img, imaging.JPEG, imaging.JPEGQuality(92)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// ImagesToPDF writes one page per image, in the given order.
func ImagesToPDF(ctx context.Context, files []File, opts ImageOptions, w io.Writer) error {
	if len(files) == 0 {
		return inputErrorf("no image provided")
	}

	imp, err := importConfig(opts)
	if err != nil {
		return err
	}

	readers := make([]io.Reader, len(files))
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := io.ReadAll(f.Data)
		if err != nil {
			return err
		}
		format, err := checkImage(f.Name, data, opts.MaxPixels)
		if err != nil {
			return err
		}
		if data, err = normalize(data, format); err != nil {
			return &InputError{Msg: f.Name + " could not be decoded", Err: err}
		}
		readers[i] = bytes.NewReader(data)
	}

	return classify(api.ImportImages(ctx, nil, w, readers, imp, newConf()), "One of the images")
}

func importConfig(opts ImageOptions) (*pdfcpu.Import, error) {
	imp := pdfcpu.DefaultImportConfig()
	if opts.PageSize == "fit" {
		return imp, nil
	}

	dim, ok := types.PaperSize[opts.PageSize]
	if !ok || (opts.PageSize != "A4" && opts.PageSize != "Letter") {
		return nil, inputErrorf("unknown page size %q", opts.PageSize)
	}
	page := *dim
	if opts.Landscape {
		page.Width, page.Height = page.Height, page.Width
	}
	imp.PageDim = &page
	imp.PageSize = fmt.Sprint(opts.PageSize)
	imp.UserDim = true
	imp.Pos = types.Center
	imp.Scale = 1
	if opts.Margin {
		imp.Scale = 0.9
	}
	return imp, nil
}
