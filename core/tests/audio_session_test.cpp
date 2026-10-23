#include "jkmeeting/audio_session.h"

#include "test_signals.h"

#include <algorithm>
#include <cassert>
#include <cstdint>
#include <cstdlib>
#include <vector>

namespace {

using jkmeeting::audio::AudioSession;
using jkmeeting::audio::CaptureFrameView;
using jkmeeting::audio::OwnedAudioFrame;
using jkmeeting::audio::SessionConfig;
using jkmeeting::audio::SessionStats;
using jkmeeting::test::sine;

constexpr int kInputRate = 48'000;
constexpr int kOutputRate = 16'000;
constexpr std::size_t kFrame = 1'024;

SessionConfig default_config() {
    return {.input_rate = kInputRate, .output_rate = kOutputRate, .channels = 1};
}

CaptureFrameView view_of(const std::vector<int16_t>& samples, std::size_t offset, std::size_t count,
                         int64_t sample_time) {
    return {
        .samples = samples.data() + offset,
        .sample_count = count,
        .sample_rate = kInputRate,
        .channels = 1,
        .sample_time = sample_time,
    };
}

struct Collected {
    std::vector<int16_t> samples;
    std::vector<int64_t> timestamps;
};

void collect(const OwnedAudioFrame& frame, Collected& collected) {
    if (frame.samples.empty()) {
        return;
    }
    assert(frame.sample_rate == kOutputRate);
    assert(frame.channels == 1);
    assert(frame.timestamp_ms == static_cast<int64_t>(collected.samples.size()) * 1'000 / kOutputRate);
    collected.timestamps.push_back(frame.timestamp_ms);
    collected.samples.insert(collected.samples.end(), frame.samples.begin(), frame.samples.end());
}

void test_dropped_frames_keep_real_duration() {
    const auto input = sine(440.0, kInputRate, kInputRate * 30, 6'000.0);
    AudioSession session(default_config());
    assert(session.valid());

    Collected collected;
    int64_t dropped = 0;
    std::size_t index = 0;
    for (std::size_t offset = 0; offset < input.size(); offset += kFrame, ++index) {
        const std::size_t count = std::min(kFrame, input.size() - offset);
        const bool is_last = offset + count == input.size();
        if (index % 10 == 9 && !is_last) {
            ++dropped;
            continue;
        }
        OwnedAudioFrame out;
        assert(session.process(view_of(input, offset, count, static_cast<int64_t>(offset)), out));
        collect(out, collected);
    }
    OwnedAudioFrame tail;
    assert(session.finish(tail));
    collect(tail, collected);

    const int64_t expected = static_cast<int64_t>(input.size()) * kOutputRate / kInputRate;
    assert(std::llabs(static_cast<int64_t>(collected.samples.size()) - expected) <= 1);

    const SessionStats stats = session.stats();
    assert(stats.gap_events == dropped);
    assert(stats.filled_gap_frames == dropped * static_cast<int64_t>(kFrame));
    assert(stats.output_frames == static_cast<int64_t>(collected.samples.size()));
    for (std::size_t i = 1; i < collected.timestamps.size(); ++i) {
        assert(collected.timestamps[i] > collected.timestamps[i - 1]);
    }
}

void test_missing_sample_time_is_contiguous() {
    const auto input = sine(440.0, kInputRate, kInputRate * 2, 6'000.0);
    AudioSession session(default_config());
    Collected collected;
    for (std::size_t offset = 0; offset < input.size(); offset += 480) {
        OwnedAudioFrame out;
        assert(session.process(view_of(input, offset, 480, -1), out));
        collect(out, collected);
    }
    OwnedAudioFrame tail;
    assert(session.finish(tail));
    collect(tail, collected);
    assert(collected.samples.size() == 32'000);
    assert(session.stats().gap_events == 0);
}

void test_gain_and_silence_stats() {
    SessionConfig config = default_config();
    config.gain_db = 6.0206F;
    AudioSession session(config);

    const std::vector<int16_t> dc(kInputRate, 4'000);
    OwnedAudioFrame out;
    assert(session.process(view_of(dc, 0, dc.size(), 0), out));
    for (std::size_t i = 200; i < out.samples.size(); ++i) {
        assert(std::abs(out.samples[i] - 8'000) <= 2);
    }
    OwnedAudioFrame tail;
    assert(session.finish(tail));

    const SessionStats stats = session.stats();
    assert(stats.speech_ms == 1'000);
    assert(stats.speech_segments == 1);
}

void test_rejects_mismatched_or_late_frames() {
    AudioSession session(default_config());
    const std::vector<int16_t> samples(960, 0);
    OwnedAudioFrame out;

    CaptureFrameView wrong_rate = view_of(samples, 0, samples.size(), 0);
    wrong_rate.sample_rate = 44'100;
    assert(!session.process(wrong_rate, out));

    CaptureFrameView wrong_channels = view_of(samples, 0, samples.size(), 0);
    wrong_channels.channels = 2;
    assert(!session.process(wrong_channels, out));

    CaptureFrameView null_samples = view_of(samples, 0, samples.size(), 0);
    null_samples.samples = nullptr;
    assert(!session.process(null_samples, out));

    assert(session.stats().rejected_frames == 3);

    assert(session.process(view_of(samples, 0, samples.size(), 0), out));
    assert(session.finish(out));
    assert(!session.finish(out));
    assert(!session.process(view_of(samples, 0, samples.size(), 960), out));
}

void test_invalid_configuration() {
    assert(!AudioSession({.input_rate = 0, .output_rate = 16'000, .channels = 1}).valid());
    assert(!AudioSession({.input_rate = 48'000, .output_rate = 16'000, .channels = 0}).valid());
    assert(!AudioSession({.input_rate = 48'000, .output_rate = 16'000, .channels = 1, .max_gap_fill_ms = -1})
                .valid());
}

}  // namespace

int main() {
    test_dropped_frames_keep_real_duration();
    test_missing_sample_time_is_contiguous();
    test_gain_and_silence_stats();
    test_rejects_mismatched_or_late_frames();
    test_invalid_configuration();
    return 0;
}
