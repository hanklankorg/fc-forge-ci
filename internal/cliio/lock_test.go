// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build unix

package cliio_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/diggsweden/forge-ci/internal/cliio"
)

// TestWithLock_SerializesReadModifyWrite is the regression for the ledger
// lost-update race: without the exclusive lock, concurrent read-modify-
// write would interleave and drop increments, so the final count would be
// less than n.
func TestWithLock_SerializesReadModifyWrite(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "counter")
	if err := os.WriteFile(counter, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}

	const n = 50

	var wg sync.WaitGroup

	for range n {
		wg.Add(1)

		go func() {
			defer wg.Done()

			err := cliio.WithLock(counter, func() error {
				b, err := os.ReadFile(counter) //nolint:gosec // test fixture path
				if err != nil {
					return err
				}

				v, err := strconv.Atoi(strings.TrimSpace(string(b)))
				if err != nil {
					return err
				}

				return os.WriteFile(counter, []byte(strconv.Itoa(v+1)), 0o600)
			})
			if err != nil {
				t.Errorf("increment counter: %v", err)
			}
		}()
	}

	wg.Wait()

	b, err := os.ReadFile(counter) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}

	got, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}

	if got != n {
		t.Errorf("counter = %d, want %d — lost updates mean locking is broken", got, n)
	}
}
