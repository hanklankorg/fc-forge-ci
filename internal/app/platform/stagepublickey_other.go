// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//go:build !unix

package platform

// syscallNoFollow has no portable equivalent outside unix; the regular-file
// check after opening still applies.
const syscallNoFollow = 0
