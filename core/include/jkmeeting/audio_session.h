#pragma once

#include "jkmeeting/audio_processor.h"
#include "jkmeeting/frame_timeline.h"
#include "jkmeeting/silence_detector.h"
#include "jkmeeting/streaming_resampler.h"

#include <cstddef>
#include <cstdint>

namespace jkmeeting::audio {

// 借用调用方的交错 S16 PCM，仅在 process 调用期间有效。
struct CaptureFrameView {
    const int16_t* samples = nullptr;
    std::size_t sample_count = 0;
    int sample_rate = 0;
    int channels = 0;
    int64_t sample_time = FrameTimeline::kInvalidSampleTime;  // 输入采样率下的采集样本位置
    int64_t host_time_ns = FrameTimeline::kInvalidHostTime;   // 到达时刻（单调时钟）
};

struct SessionConfig {
    int input_rate = 0;
    int output_rate = 0;
    int channels = 0;
    float gain_db = 0.0F;
    int max_gap_fill_ms = 1'000;
    SilenceConfig silence;
};

struct SessionStats {
    int64_t input_frames = 0;
    int64_t output_frames = 0;
    int64_t rejected_frames = 0;
    int64_t gap_events = 0;
    int64_t filled_gap_frames = 0;
    int64_t unfilled_gap_frames = 0;
    int64_t overlap_frames_discarded = 0;
    double jitter_ms = 0.0;
    double max_transit_delta_ms = 0.0;
    int64_t speech_ms = 0;
    int64_t silence_ms = 0;
    int64_t speech_segments = 0;
};

// 一次录音对应一个会话：时间轴 -> 流式重采样 -> 增益 -> 静音检测。
// 输出时间戳由累计输出帧数推导，单调且与样本对齐。
class AudioSession {
public:
    explicit AudioSession(const SessionConfig& config);

    [[nodiscard]] bool valid() const { return valid_; }

    // 返回 false 表示帧被拒绝（格式不符、非法输入或已 finish）；
    // 返回 true 时 out 可能为空，表示暂无可输出的样本。
    bool process(const CaptureFrameView& input, OwnedAudioFrame& out);

    // 冲刷重采样尾部并结束静音检测；之后 process 一律返回 false。
    bool finish(OwnedAudioFrame& out);

    [[nodiscard]] SessionStats stats() const;

private:
    void emit(std::vector<int16_t>&& samples, OwnedAudioFrame& out);

    SessionConfig config_;
    bool valid_ = false;
    bool finished_ = false;
    FrameTimeline timeline_;
    StreamingResampler resampler_;
    SilenceDetector silence_;
    int64_t emitted_frames_ = 0;
    int64_t rejected_frames_ = 0;
};

}  // namespace jkmeeting::audio
