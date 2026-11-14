// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//! The library crate: it has no binary of its own and is released as part
//! of the command that links it.

/// Returns the greeting for `name`, or for the world when `name` is empty.
pub fn greet(name: &str) -> String {
    let who = if name.is_empty() { "world" } else { name };
    format!("Hello, {who}!")
}

#[cfg(test)]
mod tests {
    use super::greet;

    #[test]
    fn greets_by_name() {
        assert_eq!(greet("Ada"), "Hello, Ada!");
    }

    #[test]
    fn greets_the_world_without_a_name() {
        assert_eq!(greet(""), "Hello, world!");
    }
}
