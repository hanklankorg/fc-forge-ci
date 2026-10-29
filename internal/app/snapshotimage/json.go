// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package snapshotimage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/diggsweden/forge-ci/internal/pathsafe"
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
		return nil, fmt.Errorf("snapshot JSON must be a regular non-symlink file")
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
		return nil, fmt.Errorf("snapshot JSON changed while opening")
	}
	const limit = 64 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("snapshot JSON must be a bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, fmt.Errorf("snapshot JSON exceeds size limit")
	}
	return body, nil
}

// encoding/json permits duplicate object keys; the frozen handoff does not.
func uniqueJSONKeys(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	var value func(int) error
	value = func(depth int) error {
		if depth > 1000 {
			return fmt.Errorf("snapshot JSON nesting exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return fmt.Errorf("unexpected snapshot JSON delimiter")
		}
		keys := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return fmt.Errorf("duplicate or invalid snapshot JSON key %q", key)
				}
				keys[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON document")
	}
	return nil
}
