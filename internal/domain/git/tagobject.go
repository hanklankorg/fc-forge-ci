// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package git

import "strings"

// TagObjectName returns the name an annotated tag object carries in its own
// `tag` header. The name is part of what the tagger signed, so it is what
// binds a signature to one release; the ref a tag is found under is not.
func TagObjectName(body string) (string, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		if name, ok := strings.CutPrefix(line, "tag "); ok {
			return name, true
		}

		if line == "" {
			break
		}
	}

	return "", false
}
