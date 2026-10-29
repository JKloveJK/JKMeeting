// JNI 桥接：只做 Kotlin <-> C++ 类型转换，不复制任何 core 业务逻辑。
// 接口面与 Apple 侧 ObjC++ 桥接等价（create/process/finish/stats），便于双端行为对比。
#include "jkmeeting/audio_session.h"

#include <jni.h>

#include <cstdint>
#include <memory>
#include <vector>

namespace {

using jkmeeting::audio::AudioSession;
using jkmeeting::audio::CaptureFrameView;
using jkmeeting::audio::OwnedAudioFrame;
using jkmeeting::audio::SessionConfig;

struct SessionHandle {
    std::unique_ptr<AudioSession> session;
    int input_rate = 0;
};

SessionHandle* FromHandle(jlong handle) {
    return reinterpret_cast<SessionHandle*>(static_cast<intptr_t>(handle));
}

jshortArray ToShortArray(JNIEnv* env, const OwnedAudioFrame& frame) {
    auto* array = env->NewShortArray(static_cast<jsize>(frame.samples.size()));
    if (array != nullptr && !frame.samples.empty()) {
        env->SetShortArrayRegion(array, 0, static_cast<jsize>(frame.samples.size()),
                                 reinterpret_cast<const jshort*>(frame.samples.data()));
    }
    return array;
}

}  // namespace

extern "C" JNIEXPORT jlong JNICALL
Java_com_jkmeeting_android_NativeCore_createSession(JNIEnv* /*env*/, jobject /*thiz*/,
                                                    jint input_rate, jint output_rate) {
    SessionConfig config;
    config.input_rate = input_rate;
    config.output_rate = output_rate;
    config.channels = 1;
    auto session = std::make_unique<AudioSession>(config);
    if (!session->valid()) {
        return 0;
    }
    auto* holder = new SessionHandle{std::move(session), input_rate};
    return static_cast<jlong>(reinterpret_cast<intptr_t>(holder));
}

extern "C" JNIEXPORT void JNICALL
Java_com_jkmeeting_android_NativeCore_destroySession(JNIEnv* /*env*/, jobject /*thiz*/,
                                                     jlong handle) {
    delete FromHandle(handle);
}

extern "C" JNIEXPORT jshortArray JNICALL
Java_com_jkmeeting_android_NativeCore_processFrame(JNIEnv* env, jobject /*thiz*/, jlong handle,
                                                   jshortArray samples, jlong sample_time) {
    SessionHandle* holder = FromHandle(handle);
    if (holder == nullptr || holder->session == nullptr) {
        return ToShortArray(env, OwnedAudioFrame{});
    }
    const jsize length = env->GetArrayLength(samples);
    std::vector<int16_t> input(static_cast<std::size_t>(length));
    if (length > 0) {
        env->GetShortArrayRegion(samples, 0, length, reinterpret_cast<jshort*>(input.data()));
    }
    CaptureFrameView view;
    view.samples = input.data();
    view.sample_count = input.size();
    view.sample_rate = holder->input_rate;
    view.channels = 1;
    view.sample_time = static_cast<int64_t>(sample_time);
    OwnedAudioFrame out;
    if (!holder->session->process(view, out)) {
        return ToShortArray(env, OwnedAudioFrame{});
    }
    return ToShortArray(env, out);
}

extern "C" JNIEXPORT jshortArray JNICALL
Java_com_jkmeeting_android_NativeCore_finishSession(JNIEnv* env, jobject /*thiz*/, jlong handle) {
    SessionHandle* holder = FromHandle(handle);
    if (holder == nullptr || holder->session == nullptr) {
        return ToShortArray(env, OwnedAudioFrame{});
    }
    OwnedAudioFrame out;
    if (!holder->session->finish(out)) {
        return ToShortArray(env, OwnedAudioFrame{});
    }
    return ToShortArray(env, out);
}

// 返回 [input_frames, output_frames, gap_events, filled_gap_frames, speech_ms, silence_ms]
extern "C" JNIEXPORT jlongArray JNICALL
Java_com_jkmeeting_android_NativeCore_sessionStats(JNIEnv* env, jobject /*thiz*/, jlong handle) {
    SessionHandle* holder = FromHandle(handle);
    auto* array = env->NewLongArray(6);
    if (holder == nullptr || holder->session == nullptr) {
        return array;
    }
    const auto stats = holder->session->stats();
    const jlong values[6] = {stats.input_frames,  stats.output_frames, stats.gap_events,
                             stats.filled_gap_frames, stats.speech_ms,   stats.silence_ms};
    env->SetLongArrayRegion(array, 0, 6, values);
    return array;
}
