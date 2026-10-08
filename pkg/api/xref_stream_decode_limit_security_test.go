/*
Copyright 2026 The pdfcpu Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// xrefStreamPDF returns a one page PDF whose only cross-reference section is a
// FlateDecode xref stream, followed by pad zero bytes inside the stream.
func xrefStreamPDF(t *testing.T, pad int) []byte {
	t.Helper()
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.7\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 10 10] >>",
	}
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := pdf.Len()
	offsets[len(objects)] = xref

	// /W [1 4 2]: object 0 is free, objects 1-4 are uncompressed at offsets.
	var entries bytes.Buffer
	entries.Write([]byte{0, 0, 0, 0, 0, 0xff, 0xff})
	for _, off := range offsets {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(off))
		entries.WriteByte(1)
		entries.Write(b[:])
		entries.Write([]byte{0, 0})
	}
	entries.Write(make([]byte, pad))
	data := flateImageData(t, entries.Bytes())

	fmt.Fprintf(&pdf, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 4 2] /Root 1 0 R /Filter /FlateDecode /Length %d >>\nstream\n",
		len(objects)+1, len(objects)+2, len(data))
	pdf.Write(data)
	fmt.Fprintf(&pdf, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", xref)
	return pdf.Bytes()
}

func TestReadXRefStreamWithinDecodeLimit(t *testing.T) {
	conf := model.NewDefaultConfiguration()
	conf.Limits.MaxDecodeBytes = 1 << 20

	ctx, err := ReadContext(context.Background(), bytes.NewReader(xrefStreamPDF(t, 0)), conf)
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		t.Fatalf("EnsurePageCount: %v", err)
	}
	if ctx.PageCount != 1 {
		t.Fatalf("PageCount = %d, want 1", ctx.PageCount)
	}
}

// An xref stream is decoded before the rest of the file is read, and must be
// held to the configured decode limit like any other stream: a small file can
// otherwise inflate one into hundreds of MiB.
func TestReadXRefStreamHonorsDecodeLimit(t *testing.T) {
	const limit = 1 << 20
	pdf := xrefStreamPDF(t, 64<<20)

	conf := model.NewDefaultConfiguration()
	conf.Limits.MaxDecodeBytes = limit

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ctx, err := ReadContext(context.Background(), bytes.NewReader(pdf), conf)
	if err == nil {
		err = ctx.EnsurePageCount()
	}
	runtime.ReadMemStats(&after)

	// Whether the read fails or recovers through xref repair, it must not have
	// inflated the stream past the limit.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("reading allocated %d MiB (err %v); the %d MiB xref stream was decoded past the %d MiB limit",
			allocated>>20, err, 64, limit>>20)
	}
}
