// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//! A tiny command: `hello-cargo-cli [name]` prints a greeting from the
//! `hello-greeting` library crate. Only the standard library beside it, so
//! the build fetches no crate.

use std::env;

fn main() {
    println!("{}", message(env::args().skip(1)));
}

/// The line the command prints for its arguments: the first one is the name.
fn message(mut args: impl Iterator<Item = String>) -> String {
    hello_greeting::greet(&args.next().unwrap_or_default())
}

#[cfg(test)]
mod tests {
    use super::message;

    #[test]
    fn greets_the_first_argument() {
        let args = ["Ada".to_owned(), "ignored".to_owned()];
        assert_eq!(message(args.into_iter()), "Hello, Ada!");
    }

    #[test]
    fn greets_the_world_without_arguments() {
        assert_eq!(message(std::iter::empty()), "Hello, world!");
    }
}
