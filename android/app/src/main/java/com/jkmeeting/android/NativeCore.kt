package com.jkmeeting.android

/**
 * C++ core 的 JNI 桥。只做类型转换，业务逻辑全部在仓库 core/ 的 AudioSession。
 * 接口面与 Apple 侧 ObjC++ 桥接等价，便于双端行为对比。
 */
class NativeCore private constructor(private var handle: Long) : AutoCloseable {

    val isValid: Boolean get() = handle != 0L

    /** 喂一帧 48kHz 采集样本，返回已输出的 16kHz 样本（可能为空）。sampleTime 为输入采样率下的样本位置。 */
    fun processFrame(samples: ShortArray, sampleTime: Long): ShortArray {
        check(isValid) { "NativeCore session 已关闭" }
        return processFrame(handle, samples, sampleTime)
    }

    /** 冲刷重采样尾部，返回剩余输出样本。 */
    fun finish(): ShortArray {
        check(isValid) { "NativeCore session 已关闭" }
        return finishSession(handle)
    }

    /** [input_frames, output_frames, gap_events, filled_gap_frames, speech_ms, silence_ms] */
    fun stats(): LongArray {
        check(isValid) { "NativeCore session 已关闭" }
        return sessionStats(handle)
    }

    override fun close() {
        if (handle != 0L) {
            destroySession(handle)
            handle = 0L
        }
    }

    private external fun createSession(inputRate: Int, outputRate: Int): Long
    private external fun destroySession(handle: Long)
    private external fun processFrame(handle: Long, samples: ShortArray, sampleTime: Long): ShortArray
    private external fun finishSession(handle: Long): ShortArray
    private external fun sessionStats(handle: Long): LongArray

    companion object {
        init {
            System.loadLibrary("jkmeeting_jni")
        }

        fun create(inputRate: Int = INPUT_RATE, outputRate: Int = OUTPUT_RATE): NativeCore {
            val core = NativeCore(0L)
            val created = core.createSession(inputRate, outputRate)
            check(created != 0L) { "AudioSession 创建失败（input=$inputRate output=$outputRate）" }
            return NativeCore(created)
        }

        const val INPUT_RATE = 48_000
        const val OUTPUT_RATE = 16_000
        const val FRAME_SAMPLES = 1_024
    }
}
