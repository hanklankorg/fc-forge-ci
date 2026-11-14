// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: EUPL-1.2 OR GPL-3.0-or-later

//! Small deterministic executables for build, extraction and publication tests.

pub fn version_line(role: &str) -> String {
    let version = option_env!("FIXTURE_VERSION").unwrap_or(env!("CARGO_PKG_VERSION"));
    format!("{role} {version}")
}

pub fn run(role: &str) {
    println!("{}", version_line(role));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn all_executable_roles_identify_themselves() {
        for role in ["worker", "key-tool", "api-service", "load-generator"] {
            let line = version_line(role);
            assert!(line.starts_with(&format!("{role} ")));
            assert!(line.split_once(' ').unwrap().1.starts_with("0.1.0"));
        }
    }
}
