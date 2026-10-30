// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package version

import (
	"errors"
	"os"
)

// New engine-owned source outputs have a canonical public mode. Existing
// checkout files retain their permissions; never broaden them on a retry.
func writeNewPublicVersionFile(root *os.Root, path string, body []byte) error {
	file, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = root.Remove(path)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Chmod(0o644); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func writePlanChangelog(root *os.Root, body []byte) error {
	err := writeNewPublicVersionFile(root, defaultChangelogFile, body)
	if errors.Is(err, os.ErrExist) {
		return root.WriteFile(defaultChangelogFile, body, 0o644)
	}
	return err
}
