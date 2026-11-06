package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"time"

	"folio/internal/config"
	"folio/internal/pdfops"
)

// Uploads above this size spill from memory to the temp directory.
const multipartMemory = 8 << 20

type Server struct {
	cfg  config.Config
	jobs chan struct{}
}

func New(cfg config.Config) *Server {
	return &Server{cfg: cfg, jobs: make(chan struct{}, cfg.MaxConcurrentJobs)}
}

func (s *Server) Handler(static fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/limits", s.limits)
	mux.HandleFunc("POST /api/images-to-pdf", s.job("images-to-pdf", "images.pdf", imagesToPDF))
	mux.HandleFunc("POST /api/merge", s.job("merge", "merged.pdf", merge))
	mux.HandleFunc("POST /api/watermark", s.job("watermark", "watermarked.pdf", watermark))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) limits(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int64{
		"maxUploadBytes": s.cfg.MaxUploadBytes,
		"maxFiles":       int64(s.cfg.MaxFiles),
	})
}

type operation func(ctx context.Context, cfg config.Config, form *multipart.Form, w io.Writer) error

func (s *Server) job(name, filename string, op operation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
		if err := r.ParseMultipartForm(multipartMemory); err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				writeError(w, http.StatusRequestEntityTooLarge,
					fmt.Sprintf("The upload is limited to %d MB", s.cfg.MaxUploadBytes>>20))
				return
			}
			writeError(w, http.StatusBadRequest, "Expected a multipart form upload")
			return
		}
		defer r.MultipartForm.RemoveAll()

		if n := len(r.MultipartForm.File["files"]); n > s.cfg.MaxFiles {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The limit is %d files per job", s.cfg.MaxFiles))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.JobTimeout)
		defer cancel()

		select {
		case s.jobs <- struct{}{}:
			defer func() { <-s.jobs }()
		case <-ctx.Done():
			writeError(w, http.StatusServiceUnavailable, "The server is busy, try again in a moment")
			return
		}

		var out bytes.Buffer
		err := run(ctx, s.cfg, r.MultipartForm, &out, op)

		var input *pdfops.InputError
		isInput := errors.As(err, &input)
		if err != nil && !isInput {
			slog.Error("job failed", "op", name, "err", err)
		}
		// Input errors quote user file names, which do not belong in the logs.
		slog.Info("job", "op", name, "ms", time.Since(start).Milliseconds(), "bytes", out.Len(), "rejected", isInput)

		switch {
		case err == nil:
			h := w.Header()
			h.Set("Content-Type", "application/pdf")
			h.Set("Content-Length", strconv.Itoa(out.Len()))
			h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
			w.Write(out.Bytes())
		case isInput:
			writeError(w, http.StatusUnprocessableEntity, input.Msg)
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, http.StatusGatewayTimeout, "The job took too long and was stopped")
		case errors.Is(err, context.Canceled):
			// The client is gone, nobody reads the response.
		default:
			writeError(w, http.StatusInternalServerError, "Something went wrong on our side")
		}
	}
}

// run keeps a parser panic on a malformed file from taking the request down
// without an answer.
func run(ctx context.Context, cfg config.Config, form *multipart.Form, w io.Writer, op operation) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &pdfops.InputError{Msg: "The file could not be processed, it may be corrupted", Err: fmt.Errorf("panic: %v", p)}
		}
	}()
	return op(ctx, cfg, form, w)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func openFiles(form *multipart.Form, field string) ([]pdfops.File, func(), error) {
	headers := form.File[field]
	files := make([]pdfops.File, 0, len(headers))
	closers := make([]io.Closer, 0, len(headers))
	closeAll := func() {
		for _, c := range closers {
			c.Close()
		}
	}
	for _, h := range headers {
		f, err := h.Open()
		if err != nil {
			closeAll()
			return nil, nil, err
		}
		closers = append(closers, f)
		files = append(files, pdfops.File{Name: h.Filename, Data: f})
	}
	return files, closeAll, nil
}

func value(form *multipart.Form, key, def string) string {
	if v := form.Value[key]; len(v) > 0 && v[0] != "" {
		return v[0]
	}
	return def
}

func number(form *multipart.Form, key string, def float64) (float64, error) {
	raw := value(form, key, "")
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, &pdfops.InputError{Msg: key + " must be a number"}
	}
	return v, nil
}

func imagesToPDF(ctx context.Context, cfg config.Config, form *multipart.Form, w io.Writer) error {
	files, done, err := openFiles(form, "files")
	if err != nil {
		return err
	}
	defer done()
	return pdfops.ImagesToPDF(ctx, files, pdfops.ImageOptions{
		PageSize:  value(form, "page_size", "A4"),
		Landscape: value(form, "orientation", "portrait") == "landscape",
		Margin:    value(form, "margin", "true") == "true",
		MaxPixels: cfg.MaxImagePixels,
	}, w)
}

func merge(ctx context.Context, _ config.Config, form *multipart.Form, w io.Writer) error {
	files, done, err := openFiles(form, "files")
	if err != nil {
		return err
	}
	defer done()
	return pdfops.Merge(ctx, files, w)
}

func watermark(ctx context.Context, cfg config.Config, form *multipart.Form, w io.Writer) error {
	files, done, err := openFiles(form, "files")
	if err != nil {
		return err
	}
	defer done()
	if len(files) != 1 {
		return &pdfops.InputError{Msg: "Watermarking takes exactly one PDF file"}
	}

	opts := pdfops.WatermarkOptions{
		Text:      value(form, "text", ""),
		Color:     value(form, "color", "#808080"),
		Tiled:     value(form, "layout", "center") == "tiled",
		Pages:     value(form, "pages", ""),
		MaxPixels: cfg.MaxImagePixels,
	}
	if opts.Opacity, err = number(form, "opacity", 0.3); err != nil {
		return err
	}
	if opts.Rotation, err = number(form, "rotation", 45); err != nil {
		return err
	}
	if opts.Scale, err = number(form, "size", 0.6); err != nil {
		return err
	}

	images, doneImages, err := openFiles(form, "image")
	if err != nil {
		return err
	}
	defer doneImages()
	if len(images) > 0 {
		if opts.Image, err = io.ReadAll(images[0].Data); err != nil {
			return err
		}
	}
	return pdfops.Watermark(ctx, files[0], opts, w)
}
