// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
plugins {
    java
}

java {
    toolchain { languageVersion = JavaLanguageVersion.of(25) }
}

repositories { mavenCentral() }

dependencies {
    testImplementation(platform("org.junit:junit-bom:5.13.4"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
}

tasks.test { useJUnitPlatform() }

// The one jar of build/libs, and an executable one: the Containerfile copies
// it by pattern, and a snapshot takes every jar it finds there.
tasks.jar {
    manifest { attributes("Main-Class" to "org.example.fixture.Server") }
}

// Byte-for-byte archives: fixed entry order and timestamps.
tasks.withType<AbstractArchiveTask>().configureEach {
    isPreserveFileTimestamps = false
    isReproducibleFileOrder = true
}
