#include "jkmeeting/streaming_resampler.h"

#include "test_signals.h"

#include <algorithm>
#include <cassert>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <vector>

namespace {

using jkmeeting::audio::StreamingResampler;
using jkmeeting::test::db;
using jkmeeting::test::rms;
using jkmeeting::test::sine;

int64_t expected_output_frames(int64_t input_frames, int input_rate, int output_rate) {
    return std::llround(static_cast<double>(input_frames) * output_rate / input_rate);
}

std::vector<int16_t> run_chunked(const std::vector<int16_t>& input, int input_rate, int output_rate,
                                 const std::vector<std::size_t>& chunk_pattern) {
    StreamingResampler resampler(input_rate, output_rate, 1);
    assert(resampler.valid());
    std::vector<int16_t> output;
    std::size_t offset = 0;
    std::size_t pattern_index = 0;
    while (offset < input.size()) {
        const std::size_t chunk = std::min(chunk_pattern[pattern_index % chunk_pattern.size()],
                                           input.size() - offset);
        const auto produced = resampler.process(input.data() + offset, chunk);
        output.insert(output.end(), produced.begin(), produced.end());
        offset += chunk;
        ++pattern_index;
    }
    const auto tail = resampler.flush();
    output.insert(output.end(), tail.begin(), tail.end());
    assert(resampler.output_frames() == static_cast<int64_t>(output.size()));
    return output;
}

void test_output_count_matches_theory_for_common_rates() {
    for (const int input_rate : {48'000, 44'100}) {
        const auto input = sine(440.0, input_rate, static_cast<std::size_t>(input_rate) * 30, 8'000.0);
        const auto output = run_chunked(input, input_rate, 16'000, {1'024});
        const int64_t expected = expected_output_frames(static_cast<int64_t>(input.size()), input_rate, 16'000);
        assert(std::llabs(static_cast<int64_t>(output.size()) - expected) <= 1);
    }
}

void test_chunking_does_not_change_output() {
    const auto input = sine(700.0, 44'100, 44'100 * 3, 9'000.0);
    const auto by_1024 = run_chunked(input, 44'100, 16'000, {1'024});
    const auto by_480 = run_chunked(input, 44'100, 16'000, {480});
    const auto irregular = run_chunked(input, 44'100, 16'000, {1, 7, 333, 4'096, 2});
    assert(by_1024 == by_480);
    assert(by_1024 == irregular);
}

void test_passband_tone_keeps_amplitude() {
    const auto input = sine(1'000.0, 48'000, 48'000 * 2, 10'000.0);
    const auto output = run_chunked(input, 48'000, 16'000, {1'024});
    const double input_rms = rms(input, 4'800, input.size() - 4'800);
    const double output_rms = rms(output, 1'600, output.size() - 1'600);
    assert(std::abs(db(output_rms / input_rms)) <= 1.0);
}

void test_tone_above_output_nyquist_is_attenuated() {
    const auto input = sine(10'000.0, 48'000, 48'000 * 2, 10'000.0);
    const auto output = run_chunked(input, 48'000, 16'000, {1'024});
    const double input_rms = rms(input, 4'800, input.size() - 4'800);
    const double output_rms = rms(output, 1'600, output.size() - 1'600);
    assert(db(output_rms / input_rms) <= -40.0);
}

void test_upsampling_keeps_count_and_amplitude() {
    const auto input = sine(500.0, 16'000, 16'000 * 2, 10'000.0);
    const auto output = run_chunked(input, 16'000, 48'000, {320});
    assert(std::llabs(static_cast<int64_t>(output.size()) - 96'000) <= 1);
    const double input_rms = rms(input, 1'600, input.size() - 1'600);
    const double output_rms = rms(output, 4'800, output.size() - 4'800);
    assert(std::abs(db(output_rms / input_rms)) <= 1.0);
}

void test_stereo_channels_stay_separate() {
    StreamingResampler resampler(48'000, 16'000, 2);
    std::vector<int16_t> interleaved(48'000 * 2);
    for (std::size_t i = 0; i < interleaved.size(); i += 2) {
        interleaved[i] = 8'000;
        interleaved[i + 1] = -8'000;
    }
    auto output = resampler.process(interleaved.data(), interleaved.size());
    const auto tail = resampler.flush();
    output.insert(output.end(), tail.begin(), tail.end());
    assert(output.size() == 16'000 * 2);
    for (std::size_t frame = 200; frame < 15'800; ++frame) {
        assert(std::abs(output[frame * 2] - 8'000) <= 2);
        assert(std::abs(output[frame * 2 + 1] + 8'000) <= 2);
    }
}

void test_invalid_parameters_and_input() {
    assert(!StreamingResampler(0, 16'000, 1).valid());
    assert(!StreamingResampler(48'000, -1, 1).valid());
    assert(!StreamingResampler(48'000, 16'000, 0).valid());

    StreamingResampler resampler(48'000, 16'000, 2);
    const std::vector<int16_t> odd{1, 2, 3};
    assert(resampler.process(odd.data(), odd.size()).empty());
    assert(resampler.process(nullptr, 4).empty());
    assert(resampler.input_frames() == 0);
}

void test_process_after_flush_is_rejected() {
    StreamingResampler resampler(48'000, 16'000, 1);
    const auto input = sine(440.0, 48'000, 4'800, 5'000.0);
    (void)resampler.process(input.data(), input.size());
    (void)resampler.flush();
    assert(resampler.process(input.data(), input.size()).empty());
    assert(resampler.flush().empty());
}

}  // namespace

int main() {
    test_output_count_matches_theory_for_common_rates();
    test_chunking_does_not_change_output();
    test_passband_tone_keeps_amplitude();
    test_tone_above_output_nyquist_is_attenuated();
    test_upsampling_keeps_count_and_amplitude();
    test_stereo_channels_stay_separate();
    test_invalid_parameters_and_input();
    test_process_after_flush_is_rejected();
    return 0;
}
