#include "jkmeeting/silence_detector.h"

#include <algorithm>
#include <cmath>

namespace jkmeeting::audio {
namespace {

int64_t frames_to_ms(int64_t frames, int sample_rate) {
    return (frames * 1'000 * 2 + sample_rate) / (2 * static_cast<int64_t>(sample_rate));
}

}  // namespace

SilenceDetector::SilenceDetector(int sample_rate, int channels, SilenceConfig config) : config_(config) {
    if (sample_rate <= 0 || channels <= 0 || config.window_ms <= 0 || config.hangover_ms < 0 ||
        !std::isfinite(config.threshold_dbfs)) {
        return;
    }
    sample_rate_ = sample_rate;
    channels_ = channels;
    window_frames_ = static_cast<int64_t>(sample_rate) * config.window_ms / 1'000;
    hangover_frames_ = static_cast<int64_t>(sample_rate) * config.hangover_ms / 1'000;
}

void SilenceDetector::process(const int16_t* samples, std::size_t sample_count) {
    const auto channel_count = static_cast<std::size_t>(channels_);
    if (!valid() || finished_ || samples == nullptr || sample_count % channel_count != 0) {
        return;
    }

    for (std::size_t i = 0; i < sample_count; ++i) {
        const double value = samples[i];
        window_energy_ += value * value;
        if ((i + 1) % channel_count == 0 && ++window_filled_ == window_frames_) {
            classify_window(window_frames_);
        }
    }
}

void SilenceDetector::finish() {
    if (!valid() || finished_) {
        return;
    }
    finished_ = true;
    if (window_filled_ > 0) {
        classify_window(window_filled_);
    }
}

void SilenceDetector::classify_window(int64_t frames) {
    const double mean_square = window_energy_ / static_cast<double>(frames * channels_);
    const double rms_dbfs = mean_square > 0.0 ? 10.0 * std::log10(mean_square / (32'768.0 * 32'768.0))
                                              : -INFINITY;
    window_energy_ = 0.0;
    window_filled_ = 0;

    bool speech = false;
    if (rms_dbfs >= config_.threshold_dbfs) {
        speech = true;
        hangover_remaining_ = hangover_frames_;
    } else if (hangover_remaining_ > 0) {
        speech = true;
        hangover_remaining_ = std::max<int64_t>(0, hangover_remaining_ - frames);
    }

    if (speech) {
        if (!in_speech_) {
            ++speech_segments_;
        }
        speech_frames_ += frames;
    } else {
        silence_frames_ += frames;
    }
    in_speech_ = speech;
}

SilenceStats SilenceDetector::stats() const {
    if (!valid()) {
        return {};
    }
    return {
        .speech_ms = frames_to_ms(speech_frames_, sample_rate_),
        .silence_ms = frames_to_ms(silence_frames_, sample_rate_),
        .speech_segments = speech_segments_,
    };
}

}  // namespace jkmeeting::audio
