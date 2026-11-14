// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe

import "testing"

// Relative is the one answer every workspace-relative path check shares, so
// each refusal is stated here instead of being held only through a caller.
func TestRelative(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		"dist/app.tgz":     true,
		"app.tgz":          true,
		"./dist/app.tgz":   true,
		"dist/..app/a..b":  true,
		"":                 false,
		"/etc/passwd":      false,
		"..":               false,
		"../outside":       false,
		"dist/../outside":  false,
		"dist/..":          false,
		"dist/tab\there":   false,
		"dist/line\nbreak": false,
		"dist/carriage\r":  false,
	} {
		if got := Relative(path); got != want {
			t.Errorf("Relative(%q) = %v, want %v", path, got, want)
		}
	}
}
