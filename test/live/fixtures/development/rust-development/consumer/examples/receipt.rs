// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//! Fixture-only evidence writer, built statically before Kaniko replaces the
//! job filesystem. BusyBox supplies sha256sum after the image builds.
use std::env;
use std::error::Error;
use std::fs::{self, OpenOptions};
use std::io::{self, Write};
use std::process::Command;

const FILES: [&str; 7] = [
    "worker-image.json",
    "api-service-image.json",
    "load-generator-image.json",
    "linux-amd64/worker-linux-amd64",
    "linux-amd64/key-tool-linux-amd64",
    "linux-amd64/api-service-linux-amd64",
    "linux-amd64/load-generator-linux-amd64",
];

fn invalid(message: &str) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, message)
}

fn is_hex(value: &str) -> bool {
    value
        .bytes()
        .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn main() -> Result<(), Box<dyn Error>> {
    let source = env::var("SOURCE_SHA")?;
    if !matches!(source.len(), 40 | 64) || !is_hex(&source) {
        return Err(invalid("SOURCE_SHA must be a full lowercase Git commit ID").into());
    }
    let version = format!("0.1.0-dev.{}", &source[..12]);
    if env::var("FIXTURE_VERSION")? != version {
        return Err(invalid("FIXTURE_VERSION must match SOURCE_SHA").into());
    }

    // Only validated hexadecimal values and fixed file names enter the JSON;
    // no arbitrary text requires escaping, and no external JSON tool is needed.
    let mut entries = Vec::new();
    for name in FILES {
        let output = Command::new("sha256sum").arg(name).output()?;
        if !output.status.success() {
            return Err(invalid(&format!("cannot hash {name}")).into());
        }
        let text = String::from_utf8(output.stdout)?;
        let digest = text.split_whitespace().next().unwrap_or_default();
        if digest.len() != 64 || !is_hex(digest) {
            return Err(invalid("sha256sum returned an invalid digest").into());
        }
        entries.push(format!("    \"{name}\": \"{digest}\""));
    }
    let receipt = format!(
        "{{\n  \"sourceSHA\": \"{source}\",\n  \"version\": \"{version}\",\n  \"files\": {{\n{}\n  }}\n}}\n",
        entries.join(",\n")
    );

    // A failed hash never creates a receipt; a failed write never replaces one.
    let temporary = ".receipt.json.tmp";
    let mut file = OpenOptions::new()
        .write(true)
        .create_new(true)
        .open(temporary)?;
    let result = file.write_all(receipt.as_bytes()).and_then(|()| {
        drop(file);
        fs::rename(temporary, "receipt.json")
    });
    if result.is_err() {
        let _ = fs::remove_file(temporary);
    }
    result?;
    Ok(())
}
