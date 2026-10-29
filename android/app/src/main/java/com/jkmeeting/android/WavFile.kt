package com.jkmeeting.android

import java.io.File
import java.io.RandomAccessFile

/** 最小 WAV（PCM S16LE 单声道）写读实现，纯 Kotlin 以便 JVM 单测。 */
object WavFile {

    fun write(file: File, samples: ShortArray, sampleRate: Int) {
        require(sampleRate > 0) { "sampleRate 必须为正" }
        val dataBytes = samples.size * 2
        RandomAccessFile(file, "rw").use { out ->
            out.setLength(0)
            out.write(header(dataBytes, sampleRate))
            val buffer = ByteArray(samples.size * 2)
            for (i in samples.indices) {
                val value = samples[i].toInt()
                buffer[i * 2] = (value and 0xFF).toByte()
                buffer[i * 2 + 1] = ((value shr 8) and 0xFF).toByte()
            }
            out.write(buffer)
        }
    }

    /** 返回 (采样率, 样本)。仅支持本类写出的 44 字节头文件。 */
    fun read(file: File): Pair<Int, ShortArray> {
        val bytes = file.readBytes()
        require(bytes.size >= 44 && String(bytes, 0, 4, Charsets.US_ASCII) == "RIFF") { "不是 WAV 文件" }
        val sampleRate = int32LE(bytes, 24)
        val dataBytes = int32LE(bytes, 40)
        val count = minOf(dataBytes / 2, (bytes.size - 44) / 2)
        val samples = ShortArray(count)
        for (i in 0 until count) {
            val low = bytes[44 + i * 2].toInt() and 0xFF
            val high = bytes[44 + i * 2 + 1].toInt()
            samples[i] = ((high shl 8) or low).toShort()
        }
        return sampleRate to samples
    }

    fun durationMs(sampleCount: Int, sampleRate: Int): Long =
        if (sampleRate <= 0) 0 else sampleCount.toLong() * 1000 / sampleRate

    private fun header(dataBytes: Int, sampleRate: Int): ByteArray {
        val header = ByteArray(44)
        fun putAscii(offset: Int, text: String) {
            text.toByteArray(Charsets.US_ASCII).copyInto(header, offset)
        }
        fun put32(offset: Int, value: Int) {
            for (i in 0 until 4) header[offset + i] = ((value shr (8 * i)) and 0xFF).toByte()
        }
        fun put16(offset: Int, value: Int) {
            for (i in 0 until 2) header[offset + i] = ((value shr (8 * i)) and 0xFF).toByte()
        }
        putAscii(0, "RIFF"); put32(4, 36 + dataBytes); putAscii(8, "WAVE")
        putAscii(12, "fmt "); put32(16, 16); put16(20, 1); put16(22, 1)
        put32(24, sampleRate); put32(28, sampleRate * 2); put16(32, 2); put16(34, 16)
        putAscii(36, "data"); put32(40, dataBytes)
        return header
    }

    private fun int32LE(bytes: ByteArray, offset: Int): Int {
        var value = 0
        for (i in 0 until 4) value = value or ((bytes[offset + i].toInt() and 0xFF) shl (8 * i))
        return value
    }
}
