package pdfops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	maxTextLen   = 200
	minTiledSize = 0.15
)

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// WatermarkOptions describes a text or image watermark. Exactly one of Text and
// Image must be set. Scale is a fraction of the page width.
type WatermarkOptions struct {
	Text      string
	Image     []byte
	Color     string
	Opacity   float64
	Rotation  float64
	Scale     float64
	Tiled     bool
	Pages     string
	MaxPixels int64
}

func (o WatermarkOptions) validate() error {
	if (o.Text == "") == (len(o.Image) == 0) {
		return inputErrorf("provide either a watermark text or a watermark image")
	}
	if len([]rune(o.Text)) > maxTextLen {
		return inputErrorf("the watermark text is limited to %d characters", maxTextLen)
	}
	// The built-in fonts are single byte: pdfcpu would silently blank anything else.
	for _, r := range o.Text {
		if r > 0xFF || r < 0x20 {
			return inputErrorf("the watermark text can only use Latin characters, %q is not supported", r)
		}
	}
	if o.Text != "" && !hexColor.MatchString(o.Color) {
		return inputErrorf("the color must look like #FACC15")
	}
	if o.Opacity < 0.05 || o.Opacity > 1 {
		return inputErrorf("the opacity must be between 0.05 and 1")
	}
	if o.Rotation < -180 || o.Rotation > 180 {
		return inputErrorf("the rotation must be between -180 and 180 degrees")
	}
	if o.Scale < 0.05 || o.Scale > 1 {
		return inputErrorf("the size must be between 5%% and 100%% of the page width")
	}
	return nil
}

func (o WatermarkOptions) newWatermark(ctx context.Context, conf *model.Configuration) (*model.Watermark, error) {
	desc := fmt.Sprintf("scalefactor:%.3f rel, opacity:%.2f, rotation:%.1f, position:c", o.Scale, o.Opacity, o.Rotation)
	if len(o.Image) > 0 {
		return api.ImageWatermarkForReader(ctx, bytes.NewReader(o.Image), desc, true, false, types.POINTS)
	}
	desc += ", fontname:Helvetica-Bold, fillcolor:" + o.Color
	return pdfcpu.ParseTextWatermarkDetails(ctx, o.Text, desc, true, types.POINTS, conf)
}

// parsePages expands a selection like "1-3,7" into page numbers. An empty
// selection means every page.
func parsePages(s string, count int) ([]int, error) {
	s = strings.ReplaceAll(s, " ", "")
	seen := make(map[int]bool)
	var pages []int
	add := func(p int) {
		if !seen[p] {
			seen[p] = true
			pages = append(pages, p)
		}
	}
	if s == "" {
		for p := 1; p <= count; p++ {
			add(p)
		}
		return pages, nil
	}
	for _, part := range strings.Split(s, ",") {
		from, to, isRange := strings.Cut(part, "-")
		if !isRange {
			to = from
		}
		a, errA := strconv.Atoi(from)
		b, errB := strconv.Atoi(to)
		if errA != nil || errB != nil || a < 1 || b < a {
			return nil, inputErrorf("invalid page selection %q, use something like 1-3,7", part)
		}
		if b > count {
			return nil, inputErrorf("page %d does not exist, the document has %d pages", b, count)
		}
		for p := a; p <= b; p++ {
			add(p)
		}
	}
	return pages, nil
}

// tileOffsets lays out a staggered grid centered on the page. The offsets are
// relative to the page center because the watermark is anchored there.
func tileOffsets(dim types.Dim, scale float64) [][2]float64 {
	stepX := dim.Width * scale * 1.2
	stepY := stepX * 0.7
	var out [][2]float64
	rows := int(dim.Height/2/stepY) + 1
	cols := int(dim.Width/2/stepX) + 1
	for r := -rows; r <= rows; r++ {
		shift := 0.0
		if r%2 != 0 {
			shift = stepX / 2
		}
		for c := -cols; c <= cols; c++ {
			out = append(out, [2]float64{float64(c)*stepX + shift, float64(r) * stepY})
		}
	}
	return out
}

// Watermark stamps the selected pages. It always draws above the content, since
// a watermark behind a scanned page would be hidden by the scan itself.
func Watermark(ctx context.Context, f File, opts WatermarkOptions, w io.Writer) error {
	if err := opts.validate(); err != nil {
		return err
	}
	if err := checkPDF(f); err != nil {
		return err
	}
	if len(opts.Image) > 0 {
		if _, err := checkImage("The watermark image", opts.Image, opts.MaxPixels); err != nil {
			return err
		}
	}

	conf := newConf()
	dims, err := api.PageDims(ctx, f.Data, conf)
	if err != nil {
		return classify(err, f.Name)
	}
	if _, err := f.Data.Seek(0, io.SeekStart); err != nil {
		return err
	}
	pages, err := parsePages(opts.Pages, len(dims))
	if err != nil {
		return err
	}

	if opts.Tiled {
		if opts.Scale < minTiledSize {
			opts.Scale = minTiledSize
		}
		return tile(ctx, f, opts, dims, pages, w)
	}

	wm, err := opts.newWatermark(ctx, conf)
	if err != nil {
		return classify(err, "The watermark")
	}
	selection := make([]string, len(pages))
	for i, p := range pages {
		selection[i] = strconv.Itoa(p)
	}
	return classify(api.AddWatermarks(ctx, f.Data, w, selection, wm, conf), f.Name)
}

// blankPage returns a one page PDF with no content. pdfcpu rebuilds the missing
// cross reference table when reading it.
func blankPage(dim types.Dim) []byte {
	return fmt.Appendf(nil, "%%PDF-1.7\n"+
		"1 0 obj\n<</Type/Catalog/Pages 2 0 R>>\nendobj\n"+
		"2 0 obj\n<</Type/Pages/Kids[3 0 R]/Count 1>>\nendobj\n"+
		"3 0 obj\n<</Type/Page/Parent 2 0 R/MediaBox[0 0 %.2f %.2f]/Resources<<>>>>\nendobj\n"+
		"trailer\n<</Size 4/Root 1 0 R>>\n%%%%EOF\n", dim.Width, dim.Height)
}

// overlay composes the tile grid on a blank page. Stamping tiles straight onto
// the document costs a full pass over each page's content per tile, which takes
// seconds per page on real files.
func overlay(ctx context.Context, opts WatermarkOptions, dim types.Dim) ([]byte, error) {
	conf := stampConf()
	doc, err := api.ReadValidateAndOptimize(ctx, bytes.NewReader(blankPage(dim)), conf, nil)
	if err != nil {
		return nil, err
	}
	for _, off := range tileOffsets(dim, opts.Scale) {
		wm, err := opts.newWatermark(ctx, conf)
		if err != nil {
			return nil, err
		}
		wm.Dx, wm.Dy = off[0], off[1]
		if err := api.WatermarkContext(ctx, doc, types.IntSet{1: true}, wm); err != nil {
			return nil, err
		}
	}
	var out bytes.Buffer
	if err := api.Write(ctx, doc, &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func stampConf() *model.Configuration {
	conf := newConf()
	conf.Cmd = model.ADDWATERMARKS
	conf.OptimizeDuplicateContentStreams = false
	return conf
}

func tile(ctx context.Context, f File, opts WatermarkOptions, dims []types.Dim, pages []int, w io.Writer) error {
	conf := stampConf()
	doc, err := api.ReadValidateAndOptimize(ctx, f.Data, conf, nil)
	if err != nil {
		return classify(err, f.Name)
	}

	bySize := make(map[types.Dim]types.IntSet)
	for _, p := range pages {
		dim := dims[p-1]
		if bySize[dim] == nil {
			bySize[dim] = types.IntSet{}
		}
		bySize[dim][p] = true
	}
	for dim, set := range bySize {
		layer, err := overlay(ctx, opts, dim)
		if err != nil {
			return classify(err, "The watermark")
		}
		wm, err := api.PDFWatermarkForReadSeeker(ctx, bytes.NewReader(layer), 1,
			"scalefactor:1 abs, rotation:0, position:c, opacity:1", true, false, types.POINTS)
		if err != nil {
			return classify(err, "The watermark")
		}
		if err := api.WatermarkContext(ctx, doc, set, wm); err != nil {
			return classify(err, f.Name)
		}
	}
	return classify(api.Write(ctx, doc, w, conf), f.Name)
}
