// SPDX-FileCopyrightText: The Forge CI Authors
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package evidence

import (
	"strings"

	"github.com/diggsweden/forge-ci/internal/safeexec"
	"github.com/diggsweden/forge-ci/internal/secrettext"
)

// Diagnostic retains bounded subprocess output for failure assertions. Oversized
// output is omitted entirely so a truncated secret cannot evade redaction.
type Diagnostic struct {
	body     []byte
	overflow bool
}

func (d *Diagnostic) Write(p []byte) (int, error) {
	if len(d.body)+len(p) > 16<<10 {
		d.overflow = true
		d.body = nil
	}
	if !d.overflow {
		d.body = append(d.body, p...)
	}
	return len(p), nil
}

// Text redacts key material and credential-shaped environment values without
// printing the environment itself. Explicit synthetic credentials may be supplied.
func (d *Diagnostic) Text(env []string, secrets ...string) string {
	if d.overflow {
		return "<subprocess output omitted: exceeded 16 KiB>"
	}
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		for _, marker := range []string{"TOKEN", "PASSWORD", "SECRET", "CREDENTIAL", "AUTH", "KEY"} {
			if strings.Contains(upper, marker) {
				secrets = append(secrets, value)
				break
			}
		}
	}
	return secrettext.Redact(string(safeexec.RedactKeyMaterial(d.body)), secrets...)
}
