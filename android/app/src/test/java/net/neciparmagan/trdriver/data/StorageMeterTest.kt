package net.neciparmagan.trdriver.data

import org.junit.Assert.assertEquals
import org.junit.Test

class StorageMeterTest {
    @Test
    fun percentMath() {
        assertEquals(0, StorageMeter.percent(0, 100))
        assertEquals(0, StorageMeter.percent(50, 0))
        assertEquals(0, StorageMeter.percent(-5, 100))
        assertEquals(25, StorageMeter.percent(25, 100))
        assertEquals(100, StorageMeter.percent(150, 100))
    }

    @Test
    fun summaryLabels() {
        assertEquals(
            "Depolama: 1 B / 4 B (%25)",
            StorageMeter.summary(1, 4) { "$it B" },
        )
        assertEquals(
            "Depolama: 7 B kullanıldı",
            StorageMeter.summary(7, 0) { "$it B" },
        )
    }
}
