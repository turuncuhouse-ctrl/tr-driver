plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
    id("org.jetbrains.kotlin.plugin.serialization") version "1.9.24" apply false
    // Applied only when android/app/google-services.json exists (optional FCM).
    id("com.google.gms.google-services") version "4.4.2" apply false
}
