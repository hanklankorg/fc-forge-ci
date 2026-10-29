// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
plugins {
    base
    id("com.android.application") version "9.4.1" apply false
}

allprojects {
    group = "org.example.trial"
    version = providers.gradleProperty("versionName").get()
    tasks.withType<AbstractArchiveTask>().configureEach {
        isPreserveFileTimestamps = false
        isReproducibleFileOrder = true
    }
}
