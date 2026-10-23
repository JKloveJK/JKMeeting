#pragma once

#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <vector>

namespace jkmeeting::test {

inline std::vector<int16_t> sine(double frequency_hz, int sample_rate, std::size_t frames, double amplitude) {
    std::vector<int16_t> samples(frames);
    for (std::size_t i = 0; i < frames; ++i) {
        const double value = amplitude * std::sin(2.0 * std::numbers::pi * frequency_hz *
                                                  static_cast<double>(i) / sample_rate);
        samples[i] = static_cast<int16_t>(std::lround(value));
    }
    return samples;
}

// 语音频段多频叠加信号（300/700/1300/2100 Hz）；SILK 对单一纯正弦的波形保真度不稳定。
inline std::vector<int16_t> speech_band(int sample_rate, std::size_t frames) {
    std::vector<int16_t> samples(frames);
    const double tones[] = {300.0, 700.0, 1'300.0, 2'100.0};
    for (std::size_t i = 0; i < frames; ++i) {
        double value = 0.0;
        for (const double tone : tones) {
            value += 2'500.0 * std::sin(2.0 * std::numbers::pi * tone * static_cast<double>(i) / sample_rate);
        }
        samples[i] = static_cast<int16_t>(std::lround(value));
    }
    return samples;
}

inline double rms(const std::vector<int16_t>& samples, std::size_t begin, std::size_t end) {
    if (end <= begin) {
        return 0.0;
    }
    double sum = 0.0;
    for (std::size_t i = begin; i < end; ++i) {
        sum += static_cast<double>(samples[i]) * samples[i];
    }
    return std::sqrt(sum / static_cast<double>(end - begin));
}

inline double db(double ratio) { return 20.0 * std::log10(ratio); }

}  // namespace jkmeeting::test
