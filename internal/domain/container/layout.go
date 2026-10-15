// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

// LayoutImage identifies the bytes and build identity of a local single image.
type LayoutImage struct {
	Digest   string
	Platform string
	Revision string
}
