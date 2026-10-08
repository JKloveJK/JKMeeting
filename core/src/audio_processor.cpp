#include "jkmeeting/audio_processor.h"

#include <algorithm>
#include <cmath>
#include <limits>

namespace jkmeeting::audio {
namespace {

bool is_valid_input(const AudioFrameView& input, int target_rate) {
    return input.samples != nullptr && input.sample_count > 0 && input.sample_rate > 0 &&
           input.channels > 0 && target_rate > 0 &&
           input.sample_count % static_cast<std::size_t>(input.channels) == 0;
}

}  // namespace

OwnedAudioFrame AudioProcessor::resample(const AudioFrameView& input, int target_rate) const {
    if (!is_valid_input(input, target_rate)) {
        return {};
    }

    const std::size_t input_frames = input.sample_count / static_cast<std::size_t>(input.channels);
    const std::size_t target_rate_size = static_cast<std::size_t>(target_rate);
    if (input_frames > std::numeric_limits<std::size_t>::max() / target_rate_size) {
        return {};
    }

    const std::size_t output_frames = input_frames * target_rate_size /
                                      static_cast<std::size_t>(input.sample_rate);
    if (output_frames == 0 || output_frames > std::numeric_limits<std::size_t>::max() /
                                                static_cast<std::size_t>(input.channels)) {
        return {};
    }

    OwnedAudioFrame output{
        .samples = std::vector<int16_t>(output_frames * static_cast<std::size_t>(input.channels)),
        .sample_rate = target_rate,
        .channels = input.channels,
        .timestamp_ms = input.timestamp_ms,
    };

    for (std::size_t output_frame = 0; output_frame < output_frames; ++output_frame) {
        const std::size_t input_frame = output_frame * static_cast<std::size_t>(input.sample_rate) /
                                        target_rate_size;
        for (int channel = 0; channel < input.channels; ++channel) {
            const std::size_t channel_offset = static_cast<std::size_t>(channel);
            output.samples[output_frame * static_cast<std::size_t>(input.channels) + channel_offset] =
                input.samples[input_frame * static_cast<std::size_t>(input.channels) + channel_offset];
        }
    }

    return output;
}

void AudioProcessor::apply_gain(OwnedAudioFrame& frame, float gain_db) const {
    if (!std::isfinite(gain_db)) {
        return;
    }

    const double gain = std::pow(10.0, static_cast<double>(gain_db) / 20.0);
    if (!std::isfinite(gain)) {
        return;
    }

    for (int16_t& sample : frame.samples) {
        const double amplified = static_cast<double>(sample) * gain;
        const double clipped = std::clamp(
            amplified,
            static_cast<double>(std::numeric_limits<int16_t>::min()),
            static_cast<double>(std::numeric_limits<int16_t>::max()));
        sample = static_cast<int16_t>(std::lround(clipped));
    }
}

}  // namespace jkmeeting::audio
