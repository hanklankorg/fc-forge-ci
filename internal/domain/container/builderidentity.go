// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package container

import (
	"crypto/sha256"
	"fmt"
)

// BuilderIdentity describes the selected image-producing executable. Consumers
// can bind this material into their own cache-input policy without knowing the
// backend's executable name or running builder-specific commands.
type BuilderIdentity struct {
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// MaterialID is the opaque cache input a consumer records. The domain and NUL
// separators make the two components unambiguous without exposing their shape
// as a consumer-side schema. Changing backend or executable changes this ID.
func (identity BuilderIdentity) MaterialID() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("forge-ci-image-builder-v1\x00"+identity.Kind+"\x00"+identity.SHA256)))
}
