// SPDX-FileCopyrightText: 2026 Digg - Agency for Digital Government
// SPDX-License-Identifier: CC0-1.0
plugins {
    id("com.android.application")
}

android {
    namespace = "org.example.trial.hello"
    compileSdk = 35
    defaultConfig {
        applicationId = "org.example.trial.hello"
        minSdk = 26
        targetSdk = 35
        versionCode = providers.gradleProperty("versionCode").get().toInt()
        versionName = providers.gradleProperty("versionName").get()
    }
    flavorDimensions += "trial"
    productFlavors {
        create("demo") {
            dimension = "trial"
            applicationIdSuffix = ".demo"
        }
    }
    val keystorePath = providers.environmentVariable("ANDROID_KEYSTORE_PATH").orNull
    if (keystorePath != null) {
        signingConfigs {
            create("trialRelease") {
                storeFile = file(keystorePath)
                storePassword = providers.environmentVariable("ANDROID_KEYSTORE_PASSWORD").get()
                keyAlias = providers.environmentVariable("ANDROID_KEY_ALIAS").get()
                keyPassword = providers.environmentVariable("ANDROID_KEY_PASSWORD").get()
            }
        }
        buildTypes.named("release") {
            signingConfig = signingConfigs.getByName("trialRelease")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    testImplementation("junit:junit:4.13.2")
}
