// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

// LayoutImage identifies the bytes and build identity of a local single image.
type LayoutImage struct {
	Digest   string
	Platform string
	Revision string
	Labels   map[string]string
}

// RegistryInputRepository names a verified parent inside the builder that
// built nothing else under it: Buildah tags the imported image
// <repository>:<digest hex>, kaniko finds it in its base-image cache by
// <repository>@<digest>. A localhost name never resolves to the registry the
// parent came from.
const RegistryInputRepository = "localhost/forge-ci-input"
