// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
plugins {
    base
    id("com.android.library") version "9.4.1" apply false
}

// The version is the `version` property of gradle.properties, which Gradle
// sets on every project and Forge CI reads and bumps for a library.
allprojects {
    group = "org.example.trial"
    tasks.withType<AbstractArchiveTask>().configureEach {
        isPreserveFileTimestamps = false
        isReproducibleFileOrder = true
    }
}
