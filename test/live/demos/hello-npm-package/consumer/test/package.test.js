// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

// Test the packed, installed public API, not a source-tree import. This catches
// missing files and broken exports even when the source's unit tests pass.
test("a clean consumer can install and import the packed package", (t) => {
  const root = fileURLToPath(new URL("../", import.meta.url));
  const scratch = mkdtempSync(join(tmpdir(), "hello-npm-package-"));
  t.after(() => rmSync(scratch, { recursive: true, force: true }));
  const packed = JSON.parse(execFileSync("npm", [
    "pack", "--json", "--ignore-scripts", "--pack-destination", scratch,
  ], { cwd: root, encoding: "utf8" }));
  writeFileSync(join(scratch, "package.json"), JSON.stringify({ private: true, type: "module" }));
  execFileSync("npm", [
    "install", "--offline", "--ignore-scripts", "--no-audit", "--no-fund",
    join(scratch, packed[0].filename),
  ], { cwd: scratch, stdio: "pipe" });
  const manifest = JSON.parse(readFileSync(join(root, "package.json"), "utf8"));
  const result = execFileSync(process.execPath, ["--input-type=module", "-e", `
    import assert from 'node:assert/strict';
    import { greet } from ${JSON.stringify(manifest.name)};
    assert.equal(greet(' Forge CI '), 'Hello, Forge CI!');
    assert.throws(() => greet(' '), TypeError);
    assert.throws(() => greet(null), TypeError);
    console.log('installed package verified');
  `], { cwd: scratch, encoding: "utf8" });
  assert.match(result, /installed package verified/);
});
