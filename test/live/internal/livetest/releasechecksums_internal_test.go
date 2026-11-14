// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package livetest

import (
	"errors"
	"fmt"
	"testing"
)

func TestVerifyReleaseChecksums(t *testing.T) {
	t.Parallel()

	const line = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  app.tar.gz\n"

	readErr := errors.New("inert read failure")

	for _, tc := range []struct {
		name, document, payload string
		names                   []string
		readErr                 error
		wantErr                 bool
	}{
		{"valid", line, "abc", []string{"app.tar.gz"}, nil, false},
		{"changed bytes", line, "bad", []string{"app.tar.gz"}, nil, true},
		{"missing published file", line, "abc", nil, nil, true},
		{"duplicate asset", line, "abc", []string{"app.tar.gz", "app.tar.gz"}, nil, true},
		{"duplicate checksum", line + line, "abc", []string{"app.tar.gz"}, nil, true},
		{"empty", "", "abc", nil, nil, true},
		{"malformed", "not checksums", "abc", nil, nil, true},
		{"read failure", line, "abc", []string{"app.tar.gz"}, readErr, true},
		{"path alias", fmt.Sprintf("%064x  ../app.tar.gz\n", 0), "abc", []string{"../app.tar.gz"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := VerifyReleaseChecksums([]byte(tc.document), tc.names, func(name string) ([]byte, error) {
				if name != "app.tar.gz" {
					t.Fatalf("unsafe or missing subject reached read: %s", name)
				}

				return []byte(tc.payload), tc.readErr
			})
			if (err != nil) != tc.wantErr || tc.readErr != nil && !errors.Is(err, tc.readErr) {
				t.Fatalf("error: %v, want failure %t", err, tc.wantErr)
			}
		})
	}
}
