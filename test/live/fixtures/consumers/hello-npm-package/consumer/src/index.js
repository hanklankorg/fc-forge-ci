// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

import { greet } from "@example-org/greeting";

export { greet };

// The released package's own API, on top of its workspace member's.
export function welcome(names) {
  return names.map((name) => greet(name)).join(" ");
}
