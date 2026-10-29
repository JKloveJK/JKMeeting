package com.jkmeeting.android

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

class WavFileTest {

    @Test
    fun writeThenReadRoundTrips() {
        val samples = ShortArray(1600) { (it % 32768 - 16384).toShort() }
        val file = File.createTempFile("wav-test", ".wav")
        try {
            WavFile.write(file, samples, 16_000)
            val (rate, restored) = WavFile.read(file)
            assertEquals(16_000, rate)
            assertArrayEquals(samples, restored)
            assertEquals(100L, WavFile.durationMs(restored.size, rate))
        } finally {
            file.delete()
        }
    }

    @Test
    fun durationUsesSampleRate() {
        assertEquals(30_000L, WavFile.durationMs(480_000, 16_000))
        assertEquals(0L, WavFile.durationMs(100, 0))
    }
}
