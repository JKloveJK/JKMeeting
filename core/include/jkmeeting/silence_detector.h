#pragma once

#include <cstddef>
#include <cstdint>

namespace jkmeeting::audio {

struct SilenceConfig {
    int window_ms = 20;
    float threshold_dbfs = -45.0F;
    int hangover_ms = 300;
};

struct SilenceStats {
    int64_t speech_ms = 0;
    int64_t silence_ms = 0;
    int64_t speech_segments = 0;
};

// 基于窗口 RMS 能量的静音检测：超过阈值判为语音，并在语音结束后保持 hangover 时长。
class SilenceDetector {
public:
    SilenceDetector(int sample_rate, int channels, SilenceConfig config = {});

    [[nodiscard]] bool valid() const { return window_frames_ > 0; }

    void process(const int16_t* samples, std::size_t sample_count);

    // 按与上一窗口相同的判定规则计入不满一个窗口的尾部。
    void finish();

    [[nodiscard]] SilenceStats stats() const;

private:
    void classify_window(int64_t frames);

    int sample_rate_ = 0;
    int channels_ = 0;
    SilenceConfig config_;
    int64_t window_frames_ = 0;
    int64_t hangover_frames_ = 0;

    double window_energy_ = 0.0;
    int64_t window_filled_ = 0;
    int64_t hangover_remaining_ = 0;
    bool in_speech_ = false;
    bool finished_ = false;

    int64_t speech_frames_ = 0;
    int64_t silence_frames_ = 0;
    int64_t speech_segments_ = 0;
};

}  // namespace jkmeeting::audio
