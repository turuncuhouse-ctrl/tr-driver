package net.neciparmagan.trdriver.data

object FileLists {
    /** Yıldızlı klasörler en üstte; sonra yıldızlı dosyalar, diğer klasörler, diğer dosyalar. */
    fun sortDrive(files: List<FileEntry>): List<FileEntry> =
        files.sortedWith(
            compareBy(
                { !it.starred },
                { it.kind != "folder" },
                { it.name.lowercase() },
            ),
        )
}
