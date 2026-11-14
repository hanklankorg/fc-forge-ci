// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

// No real key material: this executable probes release identity only.
fn main() {
    println!("hello-keytool {}", env!("CARGO_PKG_VERSION"));
}
