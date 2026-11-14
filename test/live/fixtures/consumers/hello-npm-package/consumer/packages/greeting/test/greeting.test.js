// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

import assert from "node:assert/strict";
import test from "node:test";

import { greet } from "../src/index.js";

test("the member greets by name and refuses a blank one", () => {
  assert.equal(greet(" Forge CI "), "Hello, Forge CI!");
  assert.throws(() => greet(" "), TypeError);
});
