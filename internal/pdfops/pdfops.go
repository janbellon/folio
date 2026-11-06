// Package pdfops wraps pdfcpu with the validation a public upload endpoint needs.
package pdfops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func init() {
	// The default config dir needs a writable home, which the container does not have.
	api.DisableConfigDir()
}

// InputError is a failure caused by what the user sent. Its message is safe to show them.
type InputError struct {
	Msg string
	Err error
}

func (e *InputError) Error() string {
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

func (e *InputError) Unwrap() error { return e.Err }

func inputErrorf(format string, args ...any) error {
	return &InputError{Msg: fmt.Sprintf(format, args...)}
}

// File is one uploaded file.
type File struct {
	Name string
	Data io.ReadSeeker
}

func newConf() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.Offline = true
	return conf
}

// classify turns a pdfcpu failure into an InputError, leaving cancellation untouched.
func classify(err error, name string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return err
	case strings.Contains(strings.ToLower(err.Error()), "password"):
		return &InputError{Msg: name + " is password protected", Err: err}
	}
	return &InputError{Msg: name + " could not be processed, it may be corrupted", Err: err}
}

func checkPDF(f File) error {
	head := make([]byte, 1024)
	n, err := io.ReadFull(f.Data, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	if _, err := f.Data.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if !bytes.Contains(head[:n], []byte("%PDF-")) {
		return inputErrorf("%s is not a PDF file", f.Name)
	}
	return nil
}

// Merge concatenates files in the given order.
func Merge(ctx context.Context, files []File, w io.Writer) error {
	if len(files) < 2 {
		return inputErrorf("merging needs at least two PDF files")
	}
	readers := make([]io.ReadSeeker, len(files))
	for i, f := range files {
		if err := checkPDF(f); err != nil {
			return err
		}
		// Validating one by one is the only way to name the bad file in the error.
		if _, err := api.PageCount(ctx, f.Data, newConf()); err != nil {
			return classify(err, f.Name)
		}
		if _, err := f.Data.Seek(0, io.SeekStart); err != nil {
			return err
		}
		readers[i] = f.Data
	}
	return classify(api.MergeRaw(ctx, readers, w, false, newConf()), "One of the files")
}
