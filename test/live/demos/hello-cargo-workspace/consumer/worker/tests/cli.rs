// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0

#[test]
fn executable_reports_its_workspace_version() {
    let output = std::process::Command::new(env!("CARGO_BIN_EXE_hello-worker"))
        .output()
        .expect("run worker");
    assert!(output.status.success());
    assert_eq!(
        String::from_utf8(output.stdout).unwrap(),
        format!("hello-worker {}\n", env!("CARGO_PKG_VERSION"))
    );
}
