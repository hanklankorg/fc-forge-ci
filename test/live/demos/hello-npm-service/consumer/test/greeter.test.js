// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { greet } = require('../src/greeter.js');

test('greets the world by default', () => {
  assert.equal(greet(), 'Hello, world!');
});

test('greets the name given', () => {
  assert.equal(greet(' Ada '), 'Hello, Ada!');
});

test('greets the world for a blank name', () => {
  assert.equal(greet('  '), 'Hello, world!');
});
