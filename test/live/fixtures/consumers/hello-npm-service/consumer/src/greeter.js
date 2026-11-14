// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

'use strict';

// Returns a greeting for the name given, or for the world when it is blank.
function greet(name) {
  const who = typeof name === 'string' && name.trim() !== '' ? name.trim() : 'world';
  return `Hello, ${who}!`;
}

module.exports = { greet };
