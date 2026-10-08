#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace jkmeeting::audio {

struct AudioFrameView {
    const int16_t* samples = nullptr;
    std::size_t sample_count = 0;
    int sample_rate = 0;
    int channels = 0;
    int64_t timestamp_ms = 0;
};

struct OwnedAudioFrame {
    std::vector<int16_t> samples;
    int sample_rate = 0;
    int channels = 0;
    int64_t timestamp_ms = 0;
};

class AudioProcessor {
public:
    [[nodiscard]] OwnedAudioFrame resample(const AudioFrameView& input, int target_rate) const;
    void apply_gain(OwnedAudioFrame& frame, float gain_db) const;
};

}  // namespace jkmeeting::audio
