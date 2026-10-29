package com.jkmeeting.android

import android.annotation.SuppressLint
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File

data class RecordingResult(
    val file: File,
    val outputSamples: Int,
    val durationMs: Long,
    val coreElapsedMs: Long,
    val stats: LongArray,
) {
    override fun equals(other: Any?): Boolean =
        other is RecordingResult && file == other.file && outputSamples == other.outputSamples &&
            durationMs == other.durationMs && coreElapsedMs == other.coreElapsedMs && stats.contentEquals(other.stats)

    override fun hashCode(): Int = file.hashCode()
}

/**
 * AudioRecord(48k) -> 1024 样本分帧 -> JNI AudioSession(48k->16k) -> WAV。
 * 与 Apple 壳层喂帧口径一致：sample_time 用累计输入样本数，core 时间轴可直接复用。
 */
class AudioRecorder(private val outputDir: File) {

    @Volatile
    private var recording = false

    fun stop() {
        recording = false
    }

    @SuppressLint("MissingPermission") // 权限由 UI 层申请后才调用
    suspend fun record(maxSeconds: Int = 30): RecordingResult = withContext(Dispatchers.IO) {
        val minBuffer = AudioRecord.getMinBufferSize(
            NativeCore.INPUT_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT,
        )
        require(minBuffer > 0) { "AudioRecord 缓冲区不可用：$minBuffer" }
        val record = AudioRecord(
            MediaRecorder.AudioSource.MIC, NativeCore.INPUT_RATE,
            AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT,
            maxOf(minBuffer, NativeCore.FRAME_SAMPLES * 4),
        )
        check(record.state == AudioRecord.STATE_INITIALIZED) { "AudioRecord 初始化失败" }

        val core = NativeCore.create()
        val output = ArrayList<Short>(NativeCore.OUTPUT_RATE * maxSeconds)
        val startedNs = System.nanoTime()
        var sampleTime = 0L
        recording = true
        try {
            record.startRecording()
            val frame = ShortArray(NativeCore.FRAME_SAMPLES)
            val deadlineNs = startedNs + maxSeconds.toLong() * 1_000_000_000L
            while (recording && System.nanoTime() < deadlineNs) {
                val read = record.read(frame, 0, frame.size)
                if (read <= 0) continue
                val slice = if (read == frame.size) frame else frame.copyOf(read)
                val produced = core.processFrame(slice, sampleTime)
                for (sample in produced) output.add(sample)
                sampleTime += read
            }
        } finally {
            runCatching { record.stop() }
            record.release()
        }
        val tail = core.finish()
        for (sample in tail) output.add(sample)
        val stats = core.stats()
        core.close()
        val coreElapsedMs = (System.nanoTime() - startedNs) / 1_000_000

        val samples = ShortArray(output.size) { output[it] }
        outputDir.mkdirs()
        val file = File(outputDir, "recording-${System.currentTimeMillis()}.wav")
        WavFile.write(file, samples, NativeCore.OUTPUT_RATE)
        RecordingResult(
            file = file,
            outputSamples = samples.size,
            durationMs = WavFile.durationMs(samples.size, NativeCore.OUTPUT_RATE),
            coreElapsedMs = coreElapsedMs,
            stats = stats,
        )
    }
}
