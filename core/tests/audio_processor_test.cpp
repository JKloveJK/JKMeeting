#include "jkmeeting/audio_processor.h"

#include <cassert>
#include <cstdint>
#include <vector>

namespace {

using jkmeeting::audio::AudioFrameView;
using jkmeeting::audio::AudioProcessor;
using jkmeeting::audio::OwnedAudioFrame;

void test_resample_returns_owned_pcm_at_target_rate() {
    const std::vector<int16_t> input_samples{0, 1000, 2000, 3000};
    const AudioFrameView input{
        .samples = input_samples.data(),
        .sample_count = input_samples.size(),
        .sample_rate = 4,
        .channels = 1,
        .timestamp_ms = 42,
    };

    const OwnedAudioFrame output = AudioProcessor{}.resample(input, 2);

    assert(output.sample_rate == 2);
    assert(output.channels == 1);
    assert(output.timestamp_ms == 42);
    assert((output.samples == std::vector<int16_t>{0, 2000}));
}

void test_resample_rejects_invalid_input_with_empty_output() {
    const AudioFrameView invalid{
        .samples = nullptr,
        .sample_count = 4,
        .sample_rate = 16'000,
        .channels = 1,
        .timestamp_ms = 0,
    };

    const OwnedAudioFrame output = AudioProcessor{}.resample(invalid, 8'000);

    assert(output.samples.empty());
    assert(output.sample_rate == 0);
    assert(output.channels == 0);
}

void test_apply_gain_clamps_pcm_overflow() {
    OwnedAudioFrame frame{
        .samples = {10'000, -20'000, 30'000, -30'000},
        .sample_rate = 16'000,
        .channels = 1,
        .timestamp_ms = 7,
    };

    AudioProcessor{}.apply_gain(frame, 6.0206F);

    assert((frame.samples == std::vector<int16_t>{20'000, -32'768, 32'767, -32'768}));
}

}  // namespace

int main() {
    test_resample_returns_owned_pcm_at_target_rate();
    test_resample_rejects_invalid_input_with_empty_output();
    test_apply_gain_clamps_pcm_overflow();
    return 0;
}
