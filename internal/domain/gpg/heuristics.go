// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

package gpg

import (
	"encoding/hex"
	"strings"
)

// AgentConfig is the gpg-agent.conf content that import-gpg-key writes
// when a passphrase is provided. Matches the upstream
// crazy-max/ghaction-import-gpg defaults so cached passphrases live for
// the duration of the job.
//
// The leading marker comment delineates this as forge-ci's own write
// into a config file gpg owns — clig.dev §Configuration: when you modify
// configuration that isn't your program's, say so in the file so anyone
// (e.g. a developer who finds their ~/.gnupg changed) can see what wrote
// it and how to undo it.
const AgentConfig = `# Managed by forge-ci (release gpg) — passphrase pre-seeding for CI signing.
# forge-ci wrote this gpg-agent.conf; delete it to restore your own.
default-cache-ttl 21600
max-cache-ttl 31536000
allow-preset-passphrase
`

// HexEncodePassphrase returns the uppercase-hex byte representation of
// the input. UTF-8-faithful: matches `od -An -tx1 | tr -d ' \n'
// | tr 'a-f' 'A-F'` exactly. Used as the PRESET_PASSPHRASE argument to
// gpg-connect-agent (sent via stdin so the value never appears in `ps`).
func HexEncodePassphrase(passphrase string) string {
	return strings.ToUpper(hex.EncodeToString([]byte(passphrase)))
}
