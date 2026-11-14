// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package pathsafe

import "path/filepath"

// RootContains reports containment, including equality, between existing safe
// directory roots. Resolve permitted system-layout aliases before comparing;
// OpenRoot still refuses caller-controlled links in either directory's parents.
func RootContains(parent, child string) (bool, error) {
	parentRoot, err := OpenRoot(parent)
	if err != nil {
		return false, err
	}
	defer func() { _ = parentRoot.Close() }()

	childRoot, err := OpenRoot(child)
	if err != nil {
		return false, err
	}
	defer func() { _ = childRoot.Close() }()

	parentPath, err := filepath.EvalSymlinks(parentRoot.Name())
	if err != nil {
		return false, err
	}

	childPath, err := filepath.EvalSymlinks(childRoot.Name())
	if err != nil {
		return false, err
	}

	relative, err := filepath.Rel(parentPath, childPath)
	if err != nil {
		return false, err
	}

	return Relative(relative), nil
}

// DisjointRoots reports whether neither existing safe root contains the other.
func DisjointRoots(first, second string) (bool, error) {
	contains, err := RootContains(first, second)
	if err != nil || contains {
		return false, err
	}

	contains, err = RootContains(second, first)

	return !contains, err
}
