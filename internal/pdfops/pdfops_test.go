package pdfops

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const maxPixels = 10_000_000

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// jpegWithOrientation splices a minimal EXIF APP1 segment after the SOI marker.
func jpegWithOrientation(t *testing.T, w, h int, orientation byte) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	size := len(payload) + 2
	seg := append([]byte{0xFF, 0xE1, byte(size >> 8), byte(size)}, payload...)
	raw := b.Bytes()
	return append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
}

func samplePDF(t *testing.T, pages int) []byte {
	t.Helper()
	files := make([]File, pages)
	for i := range files {
		files[i] = File{Name: "p.png", Data: bytes.NewReader(pngBytes(t, 60, 80))}
	}
	var out bytes.Buffer
	err := ImagesToPDF(context.Background(), files, ImageOptions{PageSize: "A4", MaxPixels: maxPixels}, &out)
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func pageCount(t *testing.T, b []byte) int {
	t.Helper()
	n, err := api.PageCount(context.Background(), bytes.NewReader(b), newConf())
	if err != nil {
		t.Fatalf("output is not a valid PDF: %v", err)
	}
	return n
}

func wantInputError(t *testing.T, err error) {
	t.Helper()
	var input *InputError
	if !errors.As(err, &input) {
		t.Fatalf("want InputError, got %v", err)
	}
}

func TestImagesToPDFPageSizes(t *testing.T) {
	cases := []struct {
		name string
		opts ImageOptions
		w, h float64
	}{
		{"a4", ImageOptions{PageSize: "A4"}, 595, 842},
		{"a4 landscape", ImageOptions{PageSize: "A4", Landscape: true}, 842, 595},
		{"letter", ImageOptions{PageSize: "Letter", Margin: true}, 612, 792},
		{"fit", ImageOptions{PageSize: "fit"}, 300, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.opts.MaxPixels = maxPixels
			var out bytes.Buffer
			in := []File{{"a.png", bytes.NewReader(pngBytes(t, 300, 200))}}
			if err := ImagesToPDF(context.Background(), in, c.opts, &out); err != nil {
				t.Fatal(err)
			}
			dims, err := api.PageDims(context.Background(), bytes.NewReader(out.Bytes()), newConf())
			if err != nil {
				t.Fatal(err)
			}
			if dims[0].Width != c.w || dims[0].Height != c.h {
				t.Fatalf("got %vx%v, want %vx%v", dims[0].Width, dims[0].Height, c.w, c.h)
			}
		})
	}
}

func TestExifRotationIsApplied(t *testing.T) {
	data := jpegWithOrientation(t, 300, 200, 6)
	if got := jpegOrientation(data); got != 6 {
		t.Fatalf("orientation = %d", got)
	}
	var out bytes.Buffer
	in := []File{{"a.jpg", bytes.NewReader(data)}}
	if err := ImagesToPDF(context.Background(), in, ImageOptions{PageSize: "fit", MaxPixels: maxPixels}, &out); err != nil {
		t.Fatal(err)
	}
	dims, err := api.PageDims(context.Background(), bytes.NewReader(out.Bytes()), newConf())
	if err != nil {
		t.Fatal(err)
	}
	if dims[0].Width != 200 || dims[0].Height != 300 {
		t.Fatalf("got %vx%v, want 200x300", dims[0].Width, dims[0].Height)
	}
}

func TestJpegOrientationSurvivesGarbage(t *testing.T) {
	good := jpegWithOrientation(t, 10, 10, 6)
	for i := range len(good) {
		jpegOrientation(good[:i])
	}
	for _, b := range [][]byte{nil, {0xFF, 0xD8}, {0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF}} {
		if got := jpegOrientation(b); got != 1 {
			t.Fatalf("got %d", got)
		}
	}
	// IFD offset pointing far past the end of the segment.
	if got := exifOrientation([]byte{'I', 'I', 42, 0, 0xFF, 0xFF, 0xFF, 0xFF}); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestImagesRejected(t *testing.T) {
	cases := map[string]struct {
		data []byte
		opts ImageOptions
	}{
		"not an image":    {[]byte("hello"), ImageOptions{PageSize: "A4", MaxPixels: maxPixels}},
		"too many pixels": {pngBytes(t, 100, 100), ImageOptions{PageSize: "A4", MaxPixels: 100}},
		"bad page size":   {pngBytes(t, 10, 10), ImageOptions{PageSize: "A0", MaxPixels: maxPixels}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := []File{{"a.png", bytes.NewReader(c.data)}}
			wantInputError(t, ImagesToPDF(context.Background(), in, c.opts, &bytes.Buffer{}))
		})
	}
}

func TestMergeKeepsOrderAndPages(t *testing.T) {
	var out bytes.Buffer
	in := []File{{"a.pdf", bytes.NewReader(samplePDF(t, 2))}, {"b.pdf", bytes.NewReader(samplePDF(t, 3))}}
	if err := Merge(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	if got := pageCount(t, out.Bytes()); got != 5 {
		t.Fatalf("got %d pages", got)
	}
}

func TestMergeRejectsBadInput(t *testing.T) {
	good := samplePDF(t, 1)

	var enc bytes.Buffer
	conf := model.NewAESConfiguration("secret", "secret", 256)
	if err := api.Encrypt(context.Background(), bytes.NewReader(good), &enc, conf); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		bad  []byte
		want string
	}{
		"not a pdf": {[]byte("nope"), "b.pdf is not a PDF file"},
		"truncated": {good[:len(good)/2], "b.pdf could not be processed, it may be corrupted"},
		"encrypted": {enc.Bytes(), "b.pdf is password protected"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := []File{{"a.pdf", bytes.NewReader(good)}, {"b.pdf", bytes.NewReader(c.bad)}}
			err := Merge(context.Background(), in, &bytes.Buffer{})
			wantInputError(t, err)
			var input *InputError
			errors.As(err, &input)
			if input.Msg != c.want {
				t.Fatalf("got %q", input.Msg)
			}
		})
	}
}

func TestMergeNeedsTwoFiles(t *testing.T) {
	in := []File{{"a.pdf", bytes.NewReader(samplePDF(t, 1))}}
	wantInputError(t, Merge(context.Background(), in, &bytes.Buffer{}))
}

func TestCancelledContextIsNotAnInputError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := []File{{"a.pdf", bytes.NewReader(samplePDF(t, 1))}, {"b.pdf", bytes.NewReader(samplePDF(t, 1))}}
	if err := Merge(ctx, in, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func baseOpts() WatermarkOptions {
	return WatermarkOptions{Text: "CONFIDENTIEL été", Color: "#FACC15", Opacity: 0.3, Rotation: 45, Scale: 0.6, MaxPixels: maxPixels}
}

func TestWatermarkVariants(t *testing.T) {
	img := baseOpts()
	img.Text, img.Image = "", pngBytes(t, 40, 40)
	tiled := baseOpts()
	tiled.Tiled, tiled.Scale = true, 0.3
	tiledImg := img
	tiledImg.Tiled, tiledImg.Scale = true, 0.3
	subset := baseOpts()
	subset.Pages = "1, 3-3"

	for name, opts := range map[string]WatermarkOptions{
		"text": baseOpts(), "image": img, "tiled text": tiled, "tiled image": tiledImg, "subset": subset,
	} {
		t.Run(name, func(t *testing.T) {
			src := samplePDF(t, 3)
			var out bytes.Buffer
			if err := Watermark(context.Background(), File{"a.pdf", bytes.NewReader(src)}, opts, &out); err != nil {
				t.Fatal(err)
			}
			if got := pageCount(t, out.Bytes()); got != 3 {
				t.Fatalf("got %d pages", got)
			}
			if out.Len() <= len(src) {
				t.Fatal("output did not grow, nothing was stamped")
			}
		})
	}
}

func TestWatermarkRejected(t *testing.T) {
	mutate := map[string]func(*WatermarkOptions){
		"no content":     func(o *WatermarkOptions) { o.Text = "" },
		"both contents":  func(o *WatermarkOptions) { o.Image = []byte{1} },
		"non latin":      func(o *WatermarkOptions) { o.Text = "機密" },
		"newline":        func(o *WatermarkOptions) { o.Text = "a\nb" },
		"desc injection": func(o *WatermarkOptions) { o.Color = "#000000, rotation:0" },
		"opacity":        func(o *WatermarkOptions) { o.Opacity = 2 },
		"scale":          func(o *WatermarkOptions) { o.Scale = 0 },
		"rotation":       func(o *WatermarkOptions) { o.Rotation = 500 },
		"page range":     func(o *WatermarkOptions) { o.Pages = "9" },
		"page syntax":    func(o *WatermarkOptions) { o.Pages = "2-1" },
		"page zero":      func(o *WatermarkOptions) { o.Pages = "0" },
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			opts := baseOpts()
			m(&opts)
			err := Watermark(context.Background(), File{"a.pdf", bytes.NewReader(samplePDF(t, 2))}, opts, &bytes.Buffer{})
			wantInputError(t, err)
		})
	}
}

func TestWatermarkTextWithCommasAndPercent(t *testing.T) {
	opts := baseOpts()
	opts.Text = "Copy, for 100% rent: file #3"
	var out bytes.Buffer
	if err := Watermark(context.Background(), File{"a.pdf", bytes.NewReader(samplePDF(t, 1))}, opts, &out); err != nil {
		t.Fatal(err)
	}
}
