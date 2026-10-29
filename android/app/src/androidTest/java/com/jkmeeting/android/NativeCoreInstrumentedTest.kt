package com.jkmeeting.android

import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import kotlin.math.PI
import kotlin.math.sin

/**
 * JNI 与桌面 core 的一致性：30 秒 48k 输入经会话处理后，16k 输出时长应与真实时长一致（误差 ≤ 1 帧）。
 * 该口径与 core/tests/audio_session_test 及 Apple 端 RecordingPipelineTests 相同。
 */
@RunWith(AndroidJUnit4::class)
class NativeCoreInstrumentedTest {

    @Test
    fun thirtySecondsKeepsRealDuration() {
        val core = NativeCore.create()
        var outputSamples = 0L
        var sampleTime = 0L
        val totalFrames = 30 * NativeCore.INPUT_RATE / NativeCore.FRAME_SAMPLES
        for (frameIndex in 0 until totalFrames) {
            val frame = ShortArray(NativeCore.FRAME_SAMPLES) { i ->
                val t = (frameIndex * NativeCore.FRAME_SAMPLES + i).toDouble() / NativeCore.INPUT_RATE
                (sin(2 * PI * 440 * t) * 12_000).toInt().toShort()
            }
            outputSamples += core.processFrame(frame, sampleTime).size
            sampleTime += NativeCore.FRAME_SAMPLES
        }
        outputSamples += core.finish().size
        val stats = core.stats()
        core.close()

        val expected = 30L * NativeCore.OUTPUT_RATE
        assertTrue(
            "输出样本 $outputSamples 与期望 $expected 相差超过 1 帧",
            kotlin.math.abs(outputSamples - expected) <= NativeCore.FRAME_SAMPLES,
        )
        assertTrue(
            "core 统计的输出帧 ${stats[1]} 与期望 $expected 不符",
            kotlin.math.abs(stats[1] - expected) <= NativeCore.FRAME_SAMPLES,
        )
    }
}
