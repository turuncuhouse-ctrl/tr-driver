package net.neciparmagan.trdriver.data

/** How to handle an existing file with the same name in the target folder. */
enum class UploadConflictPolicy {
    /** Ask user (intake / manual uploads). */
    ASK,
    /** Auto-append " (2)" etc. — default for gallery backup. */
    RENAME,
    /** Replace remote file. */
    OVERWRITE,
    /** Skip this file. */
    SKIP,
}

class DuplicateNameException(
    val existingId: String,
    val fileName: String,
    message: String = "Dosya zaten var: $fileName",
) : java.io.IOException(message)

class SkipUploadException : java.io.IOException("Yükleme atlandı")

class UploadNetworkBlockedException(
    val reason: String,
) : java.io.IOException(reason)
