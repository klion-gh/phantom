import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Loaded from a local, gitignored properties file (see keystore.properties.example
// and the "why" note on android/keystore.properties in .gitignore) rather than
// hardcoded here - a release built without it just falls back to being unsigned
// (fine for a local assembleDebug; assembleRelease needs the real file to produce
// something Android will accept as an update over a previous release).
val keystoreProperties = Properties()
val keystorePropertiesFile = rootProject.file("keystore.properties")
if (keystorePropertiesFile.exists()) {
    keystoreProperties.load(keystorePropertiesFile.inputStream())
}

android {
    namespace = "com.phantom.vpn"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.phantom.vpn"
        minSdk = 24
        targetSdk = 34
        versionCode = 10
        versionName = "1.8.0"
    }

    signingConfigs {
        if (keystorePropertiesFile.exists()) {
            create("release") {
                storeFile = rootProject.file(keystoreProperties["storeFile"] as String)
                storePassword = keystoreProperties["storePassword"] as String
                keyAlias = keystoreProperties["keyAlias"] as String
                keyPassword = keystoreProperties["keyPassword"] as String
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            if (keystorePropertiesFile.exists()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    composeOptions {
        kotlinCompilerExtensionVersion = "1.5.14"
    }

    packaging {
        // gomobile's .aar bundles the Go runtime's .so for every ABI; keep all of them.
        jniLibs.useLegacyPackaging = false
    }
}

dependencies {
    implementation(files("libs/mobile.aar"))

    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.activity:activity-compose:1.9.0")
    implementation("androidx.security:security-crypto:1.1.0-alpha06")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")

    val composeBom = platform("androidx.compose:compose-bom:2024.06.00")
    implementation(composeBom)
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-graphics")
    implementation("androidx.compose.material3:material3")
    // Just for the bottom nav bar's two icons (mask/configs, signal bars/resources) -
    // everything else in this app uses plain Unicode glyphs instead of an icon
    // library, but there's no good glyph equivalent for either of these two.
    implementation("androidx.compose.material:material-icons-extended")
}
