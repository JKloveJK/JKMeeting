#include "jkmeeting/frame_timeline.h"

#include <algorithm>
#include <cmath>

namespace jkmeeting::audio {

FrameTimeline::FrameTimeline(int sample_rate, int max_gap_fill_ms) {
    if (sample_rate <= 0 || max_gap_fill_ms < 0) {
        return;
    }
    sample_rate_ = sample_rate;
    max_gap_fill_frames_ = static_cast<int64_t>(sample_rate) * max_gap_fill_ms / 1'000;
}

TimelineDecision FrameTimeline::submit(int64_t sample_time, std::size_t frame_count, int64_t host_time_ns) {
    TimelineDecision decision;
    if (!valid() || frame_count == 0) {
        decision.accepted = false;
        return decision;
    }

    const auto count = static_cast<int64_t>(frame_count);
    stats_.frames_received += count;

    if (sample_time < 0 || !has_origin_) {
        const int64_t start = sample_time < 0 ? next_expected_ : sample_time;
        has_origin_ = has_origin_ || sample_time >= 0;
        next_expected_ = start + count;
        update_jitter(start, host_time_ns);
        return decision;
    }

    if (sample_time > next_expected_) {
        const int64_t gap = sample_time - next_expected_;
        const int64_t filled = std::min(gap, max_gap_fill_frames_);
        decision.silence_frames_before = filled;
        ++stats_.gap_events;
        stats_.filled_gap_frames += filled;
        stats_.unfilled_gap_frames += gap - filled;
    } else if (sample_time < next_expected_) {
        const int64_t overlap = std::min(next_expected_ - sample_time, count);
        stats_.overlap_frames_discarded += overlap;
        if (overlap == count) {
            decision.accepted = false;
            update_jitter(sample_time, host_time_ns);
            return decision;
        }
        decision.skip_frames = static_cast<std::size_t>(overlap);
    }

    next_expected_ = std::max(next_expected_, sample_time + count);
    update_jitter(sample_time, host_time_ns);
    return decision;
}

void FrameTimeline::update_jitter(int64_t media_frame, int64_t host_time_ns) {
    if (host_time_ns == kInvalidHostTime) {
        return;
    }
    if (has_previous_arrival_) {
        const double arrival_delta_ms = static_cast<double>(host_time_ns - previous_host_time_ns_) / 1e6;
        const double media_delta_ms =
            static_cast<double>(media_frame - previous_media_frame_) * 1'000.0 / sample_rate_;
        const double transit_delta = std::abs(arrival_delta_ms - media_delta_ms);
        stats_.jitter_ms += (transit_delta - stats_.jitter_ms) / 16.0;
        stats_.max_transit_delta_ms = std::max(stats_.max_transit_delta_ms, transit_delta);
    }
    has_previous_arrival_ = true;
    previous_media_frame_ = media_frame;
    previous_host_time_ns_ = host_time_ns;
}

}  // namespace jkmeeting::audio
