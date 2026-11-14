// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package secrettext

import (
	"bytes"
	"io"
)

// Writer redacts complete diagnostic lines, including secrets split across
// writes. Oversized lines are suppressed rather than flushed without redaction.
// Each stream needs its own writer; Close flushes the final unterminated line.
type Writer struct {
	destination io.Writer
	secrets     []string
	pending     []byte
	dropping    bool
}

// NewWriter protects a subprocess diagnostic stream with explicit known secrets.
func NewWriter(destination io.Writer, secrets ...string) *Writer {
	return &Writer{destination: destination, secrets: secrets}
}

func (w *Writer) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')

		end := len(data)
		if index >= 0 {
			end = index + 1
		}

		if !w.dropping && len(w.pending)+end <= 64<<10 {
			w.pending = append(w.pending, data[:end]...)
		} else {
			w.dropping = true
			w.pending = nil
		}

		data = data[end:]

		if index >= 0 {
			if err := w.flush(); err != nil {
				return length - len(data), err
			}
		}
	}

	return length, nil
}

// Close flushes buffered diagnostics without closing the borrowed destination.
func (w *Writer) Close() error { return w.flush() }

func (w *Writer) flush() error {
	text := Redact(string(w.pending), w.secrets...)
	if w.dropping {
		text = "[oversized diagnostic line suppressed]\n"
	}

	w.pending, w.dropping = nil, false
	_, err := io.WriteString(w.destination, text)

	return err
}
