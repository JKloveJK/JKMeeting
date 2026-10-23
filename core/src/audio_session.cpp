#include "jkmeeting/audio_session.h"

#include <cmath>
#include <utility>

namespace jkmeeting::audio {

AudioSession::AudioSession(const SessionConfig& config)
    : config_(config),
      timeline_(config.input_rate, config.max_gap_fill_ms),
      resampler_(config.input_rate, config.output_rate, config.channels),
      silence_(config.output_rate, config.channels, config.silence) {
    valid_ = timeline_.valid() && resampler_.valid() && silence_.valid() && std::isfinite(config.gain_db);
}

bool AudioSession::process(const CaptureFrameView& input, OwnedAudioFrame& out) {
    out = {};
    const auto channel_count = static_cast<std::size_t>(config_.channels);
    if (!valid_ || finished_ || input.samples == nullptr || input.sample_count == 0 ||
        input.sample_rate != config_.input_rate || input.channels != config_.channels ||
        input.sample_count % channel_count != 0) {
        ++rejected_frames_;
        return false;
    }

    const std::size_t frame_count = input.sample_count / channel_count;
    const TimelineDecision decision = timeline_.submit(input.sample_time, frame_count, input.host_time_ns);
    if (!decision.accepted) {
        return true;
    }

    std::vector<int16_t> produced;
    if (decision.silence_frames_before > 0) {
        const std::vector<int16_t> zeros(
            static_cast<std::size_t>(decision.silence_frames_before) * channel_count, 0);
        produced = resampler_.process(zeros.data(), zeros.size());
    }
    const std::size_t skip = decision.skip_frames * channel_count;
    auto converted = resampler_.process(input.samples + skip, input.sample_count - skip);
    produced.insert(produced.end(), converted.begin(), converted.end());

    emit(std::move(produced), out);
    return true;
}

bool AudioSession::finish(OwnedAudioFrame& out) {
    out = {};
    if (!valid_ || finished_) {
        return false;
    }
    finished_ = true;
    emit(resampler_.flush(), out);
    silence_.finish();
    return true;
}

void AudioSession::emit(std::vector<int16_t>&& samples, OwnedAudioFrame& out) {
    if (samples.empty()) {
        return;
    }
    out.samples = std::move(samples);
    out.sample_rate = config_.output_rate;
    out.channels = config_.channels;
    out.timestamp_ms = emitted_frames_ * 1'000 / config_.output_rate;

    if (config_.gain_db != 0.0F) {
        AudioProcessor{}.apply_gain(out, config_.gain_db);
    }
    silence_.process(out.samples.data(), out.samples.size());
    emitted_frames_ += static_cast<int64_t>(out.samples.size() / static_cast<std::size_t>(config_.channels));
}

SessionStats AudioSession::stats() const {
    const TimelineStats& timeline = timeline_.stats();
    const SilenceStats silence = silence_.stats();
    return {
        .input_frames = timeline.frames_received,
        .output_frames = emitted_frames_,
        .rejected_frames = rejected_frames_,
        .gap_events = timeline.gap_events,
        .filled_gap_frames = timeline.filled_gap_frames,
        .unfilled_gap_frames = timeline.unfilled_gap_frames,
        .overlap_frames_discarded = timeline.overlap_frames_discarded,
        .jitter_ms = timeline.jitter_ms,
        .max_transit_delta_ms = timeline.max_transit_delta_ms,
        .speech_ms = silence.speech_ms,
        .silence_ms = silence.silence_ms,
        .speech_segments = silence.speech_segments,
    };
}

}  // namespace jkmeeting::audio
