#pragma once

#include <cstddef>
#include <cstdint>

namespace jkmeeting::audio {

struct TimelineDecision {
    int64_t silence_frames_before = 0;  // 本帧之前需补的静音帧数
    std::size_t skip_frames = 0;        // 本帧开头需裁掉的重叠帧数
    bool accepted = true;               // false 表示整帧重叠，应丢弃
};

struct TimelineStats {
    int64_t frames_received = 0;
    int64_t gap_events = 0;
    int64_t filled_gap_frames = 0;
    int64_t unfilled_gap_frames = 0;
    int64_t overlap_frames_discarded = 0;
    double jitter_ms = 0.0;             // RFC 3550 §6.4.1 平滑到达抖动
    double max_transit_delta_ms = 0.0;  // 单帧 |到达间隔 - 媒体间隔| 的最大值
};

// 以采集样本位置为准维护连续时间轴：间隙补静音（有上限），重叠裁剪，并统计到达抖动。
class FrameTimeline {
public:
    static constexpr int64_t kInvalidSampleTime = -1;
    static constexpr int64_t kInvalidHostTime = 0;

    FrameTimeline(int sample_rate, int max_gap_fill_ms);

    [[nodiscard]] bool valid() const { return sample_rate_ > 0 && max_gap_fill_frames_ >= 0; }

    // sample_time < 0 时按连续流处理；host_time_ns == 0 时不参与抖动统计。
    TimelineDecision submit(int64_t sample_time, std::size_t frame_count, int64_t host_time_ns);

    [[nodiscard]] const TimelineStats& stats() const { return stats_; }

private:
    void update_jitter(int64_t media_frame, int64_t host_time_ns);

    int sample_rate_ = 0;
    int64_t max_gap_fill_frames_ = -1;
    bool has_origin_ = false;
    int64_t next_expected_ = 0;

    bool has_previous_arrival_ = false;
    int64_t previous_media_frame_ = 0;
    int64_t previous_host_time_ns_ = 0;

    TimelineStats stats_;
};

}  // namespace jkmeeting::audio
