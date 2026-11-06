package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"folio/internal/config"
	"folio/internal/pdfops"
)

func testConfig() config.Config {
	return config.Config{
		MaxUploadBytes:    1 << 20,
		MaxFiles:          3,
		MaxConcurrentJobs: 2,
		JobTimeout:        5 * time.Second,
		MaxImagePixels:    1_000_000,
	}
}

func newTestServer(t *testing.T, cfg config.Config) *httptest.Server {
	t.Helper()
	static := fstest.MapFS{"index.html": {Data: []byte("<html>")}}
	ts := httptest.NewServer(New(cfg).Handler(static))
	t.Cleanup(ts.Close)
	return ts
}

func pngFile(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 30, 30))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pdfFile(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	in := []pdfops.File{{Name: "a.png", Data: bytes.NewReader(pngFile(t))}}
	opts := pdfops.ImageOptions{PageSize: "A4", MaxPixels: 1_000_000}
	if err := pdfops.ImagesToPDF(context.Background(), in, opts, &out); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func post(t *testing.T, url string, fields map[string]string, files ...[]byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	for _, f := range files {
		part, _ := mw.CreateFormFile("files", "upload")
		part.Write(f)
	}
	mw.Close()
	resp, err := http.Post(url, mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func errorOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	return e.Error
}

func TestEndpointsReturnPDF(t *testing.T) {
	ts := newTestServer(t, testConfig())
	pdf, img := pdfFile(t), pngFile(t)
	cases := []struct {
		path   string
		fields map[string]string
		files  [][]byte
	}{
		{"/api/images-to-pdf", map[string]string{"page_size": "Letter", "orientation": "landscape"}, [][]byte{img, img}},
		{"/api/merge", nil, [][]byte{pdf, pdf}},
		{"/api/watermark", map[string]string{"text": "DRAFT", "layout": "tiled", "size": "0.3"}, [][]byte{pdf}},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			resp := post(t, ts.URL+c.path, c.fields, c.files...)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d: %s", resp.StatusCode, errorOf(t, resp))
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
				t.Fatalf("content type %q", ct)
			}
			out, _ := io.ReadAll(resp.Body)
			if !bytes.HasPrefix(out, []byte("%PDF-")) {
				t.Fatal("body is not a PDF")
			}
		})
	}
}

func TestErrors(t *testing.T) {
	ts := newTestServer(t, testConfig())
	pdf := pdfFile(t)

	t.Run("too many files", func(t *testing.T) {
		resp := post(t, ts.URL+"/api/merge", nil, pdf, pdf, pdf, pdf)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("upload too large", func(t *testing.T) {
		resp := post(t, ts.URL+"/api/merge", nil, make([]byte, 2<<20))
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("bad file", func(t *testing.T) {
		resp := post(t, ts.URL+"/api/merge", nil, pdf, []byte("junk"))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status %d", resp.StatusCode)
		}
		if msg := errorOf(t, resp); !strings.Contains(msg, "not a PDF") {
			t.Fatalf("message %q", msg)
		}
	})
	t.Run("not multipart", func(t *testing.T) {
		resp, err := http.Post(ts.URL+"/api/merge", "text/plain", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("opacity not a number", func(t *testing.T) {
		resp := post(t, ts.URL+"/api/watermark", map[string]string{"text": "x", "opacity": "abc"}, pdf)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
	t.Run("wrong method", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/merge", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("status %d", resp.StatusCode)
		}
	})
}

func TestBusyServerAnswers503(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrentJobs = 1
	cfg.JobTimeout = 50 * time.Millisecond
	s := New(cfg)
	s.jobs <- struct{}{}
	ts := httptest.NewServer(s.Handler(fstest.MapFS{}))
	defer ts.Close()

	resp := post(t, ts.URL+"/api/merge", nil, pdfFile(t), pdfFile(t))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPanicBecomesInputError(t *testing.T) {
	err := run(context.Background(), testConfig(), nil, io.Discard,
		func(context.Context, config.Config, *multipart.Form, io.Writer) error { panic("boom") })
	if _, ok := err.(*pdfops.InputError); !ok {
		t.Fatalf("got %v", err)
	}
}

func TestConcurrentJobs(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrentJobs = 8
	ts := newTestServer(t, cfg)
	pdf := pdfFile(t)

	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			path, fields, files := "/api/merge", map[string]string(nil), [][]byte{pdf, pdf}
			if i%2 == 0 {
				path, fields, files = "/api/watermark", map[string]string{"text": "DRAFT"}, [][]byte{pdf}
			}
			resp := post(t, ts.URL+path, fields, files...)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s: status %d", path, resp.StatusCode)
			}
		})
	}
	wg.Wait()
}

func TestHealthAndHeaders(t *testing.T) {
	ts := newTestServer(t, testConfig())
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
}
