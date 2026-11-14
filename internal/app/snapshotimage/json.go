// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
)

var (
	errJSONNotRegular      = errors.New("snapshot JSON must be a regular non-symlink file")
	errJSONChangedOpening  = errors.New("snapshot JSON changed while opening")
	errJSONUnbounded       = errors.New("snapshot JSON must be a bounded regular file")
	errJSONTooLarge        = errors.New("snapshot JSON exceeds size limit")
	errJSONNestingLimit    = errors.New("snapshot JSON nesting exceeds limit")
	errJSONUnexpectedDelim = errors.New("unexpected snapshot JSON delimiter")
	errJSONDuplicateKey    = errors.New("duplicate or invalid snapshot JSON key")
	errJSONSingleDocument  = errors.New("expected exactly one JSON document")
)

func readJSONFile(path string) ([]byte, error) {
	parent, err := pathsafe.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()

	initial, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}

	if !initial.Mode().IsRegular() {
		return nil, errJSONNotRegular
	}

	file, err := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	current, err := parent.Lstat(filepath.Base(path))
	if err != nil {
		return nil, err
	}

	if !current.Mode().IsRegular() || !os.SameFile(initial, info) || !os.SameFile(current, info) {
		return nil, errJSONChangedOpening
	}

	const limit = 64 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errJSONUnbounded
	}

	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}

	if len(body) > limit {
		return nil, errJSONTooLarge
	}

	return body, nil
}

// encoding/json permits duplicate object keys; the frozen handoff does not.
func uniqueJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))

	var value func(int) error

	value = func(depth int) error {
		if depth > 1000 {
			return errJSONNestingLimit
		}

		token, err := decoder.Token()
		if err != nil {
			return err
		}

		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}

		if delim != '{' && delim != '[' {
			return errJSONUnexpectedDelim
		}

		keys := map[string]bool{}

		for decoder.More() {
			if delim == '{' {
				key, err := decoder.Token()
				if err != nil {
					return err
				}

				name, ok := key.(string)
				if !ok || keys[name] {
					return fmt.Errorf("%w %q", errJSONDuplicateKey, key)
				}

				keys[name] = true
			}

			if err := value(depth + 1); err != nil {
				return err
			}
		}

		_, err = decoder.Token()

		return err
	}
	if err := value(0); err != nil {
		return err
	}

	if _, err := decoder.Token(); err != io.EOF {
		return errJSONSingleDocument
	}

	return nil
}
