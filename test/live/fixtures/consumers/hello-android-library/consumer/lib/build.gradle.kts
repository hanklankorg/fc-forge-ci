// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
plugins {
    id("com.android.library")
    `maven-publish`
}

android {
    namespace = "org.example.trial.hello.library"
    compileSdk = 35
    defaultConfig {
        minSdk = 26
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    // The release variant is the one software component the module publishes.
    publishing {
        singleVariant("release") {
            withSourcesJar()
        }
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}

// One Maven publication of the release AAR. It names no repository and signs
// nothing: Forge CI exports it to a directory and publishes the exported files.
// The group and the version are the project's, set in the root build file and
// gradle.properties.
publishing {
    publications {
        register<MavenPublication>("release") {
            artifactId = "hello-android-library"
            // The Android Gradle Plugin creates the component after evaluation.
            afterEvaluate { from(components["release"]) }
        }
    }
}
