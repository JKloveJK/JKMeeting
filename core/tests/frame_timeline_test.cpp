#include "jkmeeting/frame_timeline.h"

#include <cassert>
#include <cmath>
#include <cstdint>

namespace {

using jkmeeting::audio::FrameTimeline;
using jkmeeting::audio::TimelineDecision;

constexpr int kRate = 48'000;
constexpr std::size_t kFrame = 480;  // 10 ms

bool near(double actual, double expected) { return std::abs(actual - expected) < 1e-6; }

void test_contiguous_frames_need_no_adjustment() {
    FrameTimeline timeline(kRate, 1'000);
    for (int64_t i = 0; i < 10; ++i) {
        const TimelineDecision decision = timeline.submit(1'000 + i * kFrame, kFrame, 0);
        assert(decision.accepted);
        assert(decision.silence_frames_before == 0);
        assert(decision.skip_frames == 0);
    }
    assert(timeline.stats().frames_received == 4'800);
    assert(timeline.stats().gap_events == 0);
}

void test_gap_is_filled_with_silence() {
    FrameTimeline timeline(kRate, 1'000);
    (void)timeline.submit(0, kFrame, 0);
    const TimelineDecision decision = timeline.submit(3 * kFrame, kFrame, 0);
    assert(decision.accepted);
    assert(decision.silence_frames_before == 2 * static_cast<int64_t>(kFrame));
    assert(timeline.stats().gap_events == 1);
    assert(timeline.stats().filled_gap_frames == 960);

    const TimelineDecision next = timeline.submit(4 * kFrame, kFrame, 0);
    assert(next.silence_frames_before == 0);
}

void test_gap_beyond_cap_is_partially_filled() {
    FrameTimeline timeline(kRate, 1'000);
    (void)timeline.submit(0, kFrame, 0);
    const TimelineDecision decision = timeline.submit(kFrame + 2 * kRate, kFrame, 0);
    assert(decision.silence_frames_before == kRate);
    assert(timeline.stats().filled_gap_frames == kRate);
    assert(timeline.stats().unfilled_gap_frames == kRate);

    const TimelineDecision next = timeline.submit(2 * kFrame + 2 * kRate, kFrame, 0);
    assert(next.silence_frames_before == 0);
    assert(next.skip_frames == 0);
}

void test_overlap_is_trimmed_or_dropped() {
    FrameTimeline timeline(kRate, 1'000);
    (void)timeline.submit(0, kFrame, 0);

    const TimelineDecision partial = timeline.submit(kFrame - 100, kFrame, 0);
    assert(partial.accepted);
    assert(partial.skip_frames == 100);

    const TimelineDecision full = timeline.submit(kFrame, 200, 0);
    assert(!full.accepted);
    assert(timeline.stats().overlap_frames_discarded == 300);

    const TimelineDecision resumed = timeline.submit(2 * kFrame - 100, kFrame, 0);
    assert(resumed.accepted);
    assert(resumed.skip_frames == 0);
    assert(resumed.silence_frames_before == 0);
}

void test_invalid_sample_time_is_treated_as_contiguous() {
    FrameTimeline timeline(kRate, 1'000);
    for (int i = 0; i < 5; ++i) {
        const TimelineDecision decision = timeline.submit(FrameTimeline::kInvalidSampleTime, kFrame, 0);
        assert(decision.accepted);
        assert(decision.silence_frames_before == 0);
        assert(decision.skip_frames == 0);
    }
    assert(timeline.stats().gap_events == 0);
}

void test_jitter_follows_rfc3550_smoothing() {
    constexpr int64_t kFrameNs = 10'000'000;  // 480 帧 @ 48 kHz
    constexpr int64_t kBase = 5'000'000'000;
    FrameTimeline timeline(kRate, 1'000);
    (void)timeline.submit(0, kFrame, kBase);
    (void)timeline.submit(kFrame, kFrame, kBase + kFrameNs);
    assert(near(timeline.stats().jitter_ms, 0.0));

    (void)timeline.submit(2 * kFrame, kFrame, kBase + 2 * kFrameNs + 8'000'000);
    assert(near(timeline.stats().jitter_ms, 0.5));

    (void)timeline.submit(3 * kFrame, kFrame, kBase + 3 * kFrameNs);
    assert(near(timeline.stats().jitter_ms, 0.5 + (8.0 - 0.5) / 16.0));
    assert(near(timeline.stats().max_transit_delta_ms, 8.0));

    (void)timeline.submit(4 * kFrame, kFrame, FrameTimeline::kInvalidHostTime);
    assert(near(timeline.stats().jitter_ms, 0.5 + (8.0 - 0.5) / 16.0));
}

void test_invalid_configuration() {
    assert(!FrameTimeline(0, 1'000).valid());
    assert(!FrameTimeline(kRate, -1).valid());
    assert(FrameTimeline(kRate, 0).valid());
}

}  // namespace

int main() {
    test_contiguous_frames_need_no_adjustment();
    test_gap_is_filled_with_silence();
    test_gap_beyond_cap_is_partially_filled();
    test_overlap_is_trimmed_or_dropped();
    test_invalid_sample_time_is_treated_as_contiguous();
    test_jitter_follows_rfc3550_smoothing();
    test_invalid_configuration();
    return 0;
}
