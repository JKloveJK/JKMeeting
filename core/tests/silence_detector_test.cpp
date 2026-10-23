#include "jkmeeting/silence_detector.h"

#include "test_signals.h"

#include <algorithm>
#include <cassert>
#include <cstdint>
#include <random>
#include <vector>

namespace {

using jkmeeting::audio::SilenceDetector;
using jkmeeting::audio::SilenceStats;
using jkmeeting::test::sine;

constexpr int kRate = 16'000;

std::vector<int16_t> silence(std::size_t frames) { return std::vector<int16_t>(frames, 0); }

std::vector<int16_t> noise(std::size_t frames, int amplitude) {
    std::mt19937 generator(42);
    std::uniform_int_distribution<int> distribution(-amplitude, amplitude);
    std::vector<int16_t> samples(frames);
    for (auto& sample : samples) {
        sample = static_cast<int16_t>(distribution(generator));
    }
    return samples;
}

std::vector<int16_t> concat(std::initializer_list<std::vector<int16_t>> parts) {
    std::vector<int16_t> result;
    for (const auto& part : parts) {
        result.insert(result.end(), part.begin(), part.end());
    }
    return result;
}

SilenceStats detect(const std::vector<int16_t>& samples, std::size_t chunk) {
    SilenceDetector detector(kRate, 1);
    assert(detector.valid());
    for (std::size_t offset = 0; offset < samples.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, samples.size() - offset);
        detector.process(samples.data() + offset, count);
    }
    detector.finish();
    return detector.stats();
}

void test_pure_silence() {
    const SilenceStats stats = detect(silence(kRate), 320);
    assert(stats.speech_ms == 0);
    assert(stats.silence_ms == 1'000);
    assert(stats.speech_segments == 0);
}

void test_pure_tone_is_speech() {
    // 振幅 3277 约为 -20 dBFS 峰值
    const SilenceStats stats = detect(sine(440.0, kRate, kRate, 3'277.0), 320);
    assert(stats.speech_ms == 1'000);
    assert(stats.silence_ms == 0);
    assert(stats.speech_segments == 1);
}

void test_low_level_noise_is_silence() {
    // 峰值 30 的均匀噪声 RMS 约 -63 dBFS，低于 -45 dBFS 阈值
    const SilenceStats stats = detect(noise(kRate, 30), 333);
    assert(stats.speech_ms == 0);
}

void test_alternating_segments_with_hangover() {
    const auto samples = concat({silence(kRate), sine(300.0, kRate, kRate, 6'000.0), silence(kRate),
                                 noise(kRate / 2, 8'000), silence(kRate)});
    const SilenceStats stats = detect(samples, 333);
    const int64_t expected_speech = 1'000 + 500;
    const int64_t tolerance = 2 * (20 + 300);
    assert(stats.speech_ms >= expected_speech);
    assert(stats.speech_ms <= expected_speech + tolerance);
    assert(stats.speech_ms + stats.silence_ms == 4'500);
    assert(stats.speech_segments == 2);
}

void test_chunking_does_not_change_result() {
    const auto samples = concat({silence(5'000), sine(300.0, kRate, 7'000, 6'000.0), silence(9'001)});
    const SilenceStats a = detect(samples, 1);
    const SilenceStats b = detect(samples, 320);
    const SilenceStats c = detect(samples, 4'097);
    assert(a.speech_ms == b.speech_ms && b.speech_ms == c.speech_ms);
    assert(a.silence_ms == b.silence_ms && b.silence_ms == c.silence_ms);
}

void test_partial_tail_window_is_counted() {
    const SilenceStats stats = detect(silence(330), 330);
    assert(stats.silence_ms == 21);
}

void test_invalid_configuration() {
    assert(!SilenceDetector(0, 1).valid());
    assert(!SilenceDetector(kRate, 0).valid());
    assert(!SilenceDetector(kRate, 1, {.window_ms = 0}).valid());
}

}  // namespace

int main() {
    test_pure_silence();
    test_pure_tone_is_speech();
    test_low_level_noise_is_silence();
    test_alternating_segments_with_hangover();
    test_chunking_does_not_change_result();
    test_partial_tail_window_is_counted();
    test_invalid_configuration();
    return 0;
}
