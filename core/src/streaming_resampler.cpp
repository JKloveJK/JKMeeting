#include "jkmeeting/streaming_resampler.h"

#include <algorithm>
#include <cmath>
#include <limits>
#include <numbers>
#include <numeric>

namespace jkmeeting::audio {
namespace {

double sinc(double x) {
    if (std::abs(x) < 1e-12) {
        return 1.0;
    }
    const double pi_x = std::numbers::pi * x;
    return std::sin(pi_x) / pi_x;
}

double blackman(double distance, double half_width) {
    if (std::abs(distance) >= half_width) {
        return 0.0;
    }
    const double x = std::numbers::pi * distance / half_width;
    return 0.42 + 0.5 * std::cos(x) + 0.08 * std::cos(2.0 * x);
}

int16_t to_int16(float value) {
    const float clipped = std::clamp(value, static_cast<float>(std::numeric_limits<int16_t>::min()),
                                     static_cast<float>(std::numeric_limits<int16_t>::max()));
    return static_cast<int16_t>(std::lround(clipped));
}

}  // namespace

StreamingResampler::StreamingResampler(int input_rate, int output_rate, int channels) {
    if (input_rate <= 0 || output_rate <= 0 || channels <= 0) {
        return;
    }

    const int64_t divisor = std::gcd(static_cast<int64_t>(input_rate), static_cast<int64_t>(output_rate));
    up_ = output_rate / divisor;
    down_ = input_rate / divisor;
    channels_ = channels;
    phase_count_ = static_cast<int>(std::min<int64_t>(up_, kMaxPhases));

    const double ratio = std::max(1.0, static_cast<double>(input_rate) / output_rate);
    half_taps_ = static_cast<int>(std::ceil(kHalfTaps * ratio));

    // 截止频率取较低采样率的 0.45 倍（Nyquist 的 90%），以输入采样率归一化。
    const double cutoff = 0.45 * std::min(input_rate, output_rate) / input_rate;
    const int taps = 2 * half_taps_;
    coefficients_.resize(static_cast<std::size_t>(phase_count_) * static_cast<std::size_t>(taps));
    for (int phase = 0; phase < phase_count_; ++phase) {
        const double fraction = static_cast<double>(phase) / phase_count_;
        double sum = 0.0;
        float* row = coefficients_.data() + static_cast<std::size_t>(phase) * static_cast<std::size_t>(taps);
        for (int tap = 0; tap < taps; ++tap) {
            const double distance = static_cast<double>(tap - half_taps_ + 1) - fraction;
            const double weight = 2.0 * cutoff * sinc(2.0 * cutoff * distance) * blackman(distance, half_taps_);
            row[tap] = static_cast<float>(weight);
            sum += weight;
        }
        for (int tap = 0; tap < taps; ++tap) {
            row[tap] = static_cast<float>(row[tap] / sum);
        }
    }

    history_.assign(static_cast<std::size_t>(half_taps_) * static_cast<std::size_t>(channels_), 0.0F);
    base_frame_ = 0;
    next_position_ = half_taps_;
    valid_ = true;
}

std::vector<int16_t> StreamingResampler::process(const int16_t* samples, std::size_t sample_count) {
    const auto channel_count = static_cast<std::size_t>(channels_);
    if (!valid_ || flushed_ || samples == nullptr || sample_count == 0 || sample_count % channel_count != 0) {
        return {};
    }

    const std::size_t frame_count = sample_count / channel_count;
    append_input(samples, frame_count);
    input_frames_ += static_cast<int64_t>(frame_count);

    std::vector<int16_t> out;
    produce(out, std::numeric_limits<int64_t>::max());
    return out;
}

std::vector<int16_t> StreamingResampler::flush() {
    if (!valid_ || flushed_) {
        return {};
    }
    flushed_ = true;

    append_zeros(static_cast<std::size_t>(half_taps_));
    const int64_t target = (input_frames_ * up_ * 2 + down_) / (2 * down_);
    std::vector<int16_t> out;
    produce(out, target);
    return out;
}

void StreamingResampler::append_input(const int16_t* samples, std::size_t frame_count) {
    const std::size_t count = frame_count * static_cast<std::size_t>(channels_);
    history_.reserve(history_.size() + count);
    for (std::size_t i = 0; i < count; ++i) {
        history_.push_back(static_cast<float>(samples[i]));
    }
}

void StreamingResampler::append_zeros(std::size_t frame_count) {
    history_.insert(history_.end(), frame_count * static_cast<std::size_t>(channels_), 0.0F);
}

void StreamingResampler::produce(std::vector<int16_t>& out, int64_t max_output_frames) {
    const auto channel_count = static_cast<std::size_t>(channels_);
    const auto taps = static_cast<std::size_t>(2 * half_taps_);
    const int64_t available_end = base_frame_ + static_cast<int64_t>(history_.size() / channel_count);

    while (output_frames_ < max_output_frames && next_position_ + half_taps_ < available_end) {
        const auto phase_index = static_cast<std::size_t>(phase_ * phase_count_ / up_);
        const float* row = coefficients_.data() + phase_index * taps;
        const auto first = static_cast<std::size_t>(next_position_ - half_taps_ + 1 - base_frame_);

        for (std::size_t channel = 0; channel < channel_count; ++channel) {
            const float* source = history_.data() + first * channel_count + channel;
            float acc = 0.0F;
            for (std::size_t tap = 0; tap < taps; ++tap) {
                acc += row[tap] * source[tap * channel_count];
            }
            out.push_back(to_int16(acc));
        }

        ++output_frames_;
        phase_ += down_;
        next_position_ += phase_ / up_;
        phase_ %= up_;
    }

    const int64_t keep_from = std::min(next_position_ - half_taps_ + 1, available_end);
    if (keep_from > base_frame_) {
        const auto drop = static_cast<std::size_t>(keep_from - base_frame_) * channel_count;
        history_.erase(history_.begin(), history_.begin() + static_cast<std::ptrdiff_t>(drop));
        base_frame_ = keep_from;
    }
}

}  // namespace jkmeeting::audio
