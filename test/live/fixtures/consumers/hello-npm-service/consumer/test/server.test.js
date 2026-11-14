// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { respond } = require('../src/server.js');

test('root greets', () => {
  assert.deepEqual(respond('/'), { status: 200, body: 'Hello, world!' });
});

test('health is ok', () => {
  assert.deepEqual(respond('/health'), { status: 200, body: 'ok' });
});

test('other paths are not found', () => {
  assert.equal(respond('/missing').status, 404);
});
