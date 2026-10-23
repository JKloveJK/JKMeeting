#include "jkmeeting/opus_encoder.h"

#include <opus.h>

#include <algorithm>

namespace jkmeeting::audio {
namespace {

constexpr int kMaxPacketBytes = 1'275;

bool supported_rate(int rate) {
    return rate == 8'000 || rate == 12'000 || rate == 16'000 || rate == 24'000 || rate == 48'000;
}

bool supported_frame_ms(int frame_ms) { return frame_ms == 10 || frame_ms == 20 || frame_ms == 40 || frame_ms == 60; }

}  // namespace

struct OpusPacketEncoder::Impl {
    OpusEncoderConfig config;
    OpusEncoder* encoder = nullptr;
    int frame_size = 0;  // 每声道帧数
    int lookahead = 0;
    bool failed = false;
    bool flushed = false;
    int64_t input_frames = 0;
    int64_t encoded_frames = 0;
    std::vector<int16_t> pending;
    std::vector<uint8_t> scratch = std::vector<uint8_t>(kMaxPacketBytes);

    ~Impl() {
        if (encoder != nullptr) {
            opus_encoder_destroy(encoder);
        }
    }

    bool encode_frame(const int16_t* frame, std::vector<EncodedPacket>& out) {
        const opus_int32 bytes =
            opus_encode(encoder, frame, frame_size, scratch.data(), static_cast<opus_int32>(scratch.size()));
        if (bytes < 0) {
            failed = true;
            return false;
        }
        out.push_back(EncodedPacket{std::vector<uint8_t>(scratch.begin(), scratch.begin() + bytes), frame_size});
        encoded_frames += frame_size;
        return true;
    }
};

OpusPacketEncoder::OpusPacketEncoder(const OpusEncoderConfig& config) : impl_(std::make_unique<Impl>()) {
    impl_->config = config;
    if (!supported_rate(config.sample_rate) || config.channels < 1 || config.channels > 2 ||
        !supported_frame_ms(config.frame_ms) || config.bitrate_bps < 500 || config.bitrate_bps > 512'000) {
        return;
    }
    int error = OPUS_OK;
    impl_->encoder = opus_encoder_create(config.sample_rate, config.channels, OPUS_APPLICATION_VOIP, &error);
    if (error != OPUS_OK || impl_->encoder == nullptr) {
        impl_->encoder = nullptr;
        return;
    }
    opus_int32 lookahead = 0;
    if (opus_encoder_ctl(impl_->encoder, OPUS_SET_BITRATE(config.bitrate_bps)) != OPUS_OK ||
        opus_encoder_ctl(impl_->encoder, OPUS_SET_VBR(1)) != OPUS_OK ||
        opus_encoder_ctl(impl_->encoder, OPUS_SET_SIGNAL(OPUS_SIGNAL_VOICE)) != OPUS_OK ||
        opus_encoder_ctl(impl_->encoder, OPUS_GET_LOOKAHEAD(&lookahead)) != OPUS_OK) {
        opus_encoder_destroy(impl_->encoder);
        impl_->encoder = nullptr;
        return;
    }
    impl_->frame_size = config.sample_rate * config.frame_ms / 1'000;
    impl_->lookahead = lookahead;
}

OpusPacketEncoder::~OpusPacketEncoder() = default;

bool OpusPacketEncoder::valid() const { return impl_->encoder != nullptr; }

bool OpusPacketEncoder::encode(const int16_t* samples, std::size_t sample_count, std::vector<EncodedPacket>& out) {
    const auto channels = static_cast<std::size_t>(impl_->config.channels);
    if (!valid() || impl_->failed || impl_->flushed || (samples == nullptr && sample_count > 0) ||
        sample_count % channels != 0) {
        return false;
    }
    const std::size_t frame_samples = static_cast<std::size_t>(impl_->frame_size) * channels;
    auto& pending = impl_->pending;
    impl_->input_frames += static_cast<int64_t>(sample_count / channels);

    std::size_t offset = 0;
    if (!pending.empty()) {
        const std::size_t take = std::min(frame_samples - pending.size(), sample_count);
        pending.insert(pending.end(), samples, samples + take);
        offset = take;
        if (pending.size() < frame_samples) {
            return true;
        }
        if (!impl_->encode_frame(pending.data(), out)) {
            return false;
        }
        pending.clear();
    }
    while (sample_count - offset >= frame_samples) {
        if (!impl_->encode_frame(samples + offset, out)) {
            return false;
        }
        offset += frame_samples;
    }
    pending.insert(pending.end(), samples + offset, samples + sample_count);
    return true;
}

bool OpusPacketEncoder::flush(std::vector<EncodedPacket>& out) {
    if (!valid() || impl_->failed || impl_->flushed) {
        return false;
    }
    impl_->flushed = true;
    if (impl_->input_frames == 0) {
        return true;
    }
    const std::size_t frame_samples =
        static_cast<std::size_t>(impl_->frame_size) * static_cast<std::size_t>(impl_->config.channels);
    auto& pending = impl_->pending;
    while (impl_->encoded_frames < impl_->input_frames + impl_->lookahead) {
        pending.resize(frame_samples, 0);
        if (!impl_->encode_frame(pending.data(), out)) {
            return false;
        }
        pending.clear();
    }
    return true;
}

int OpusPacketEncoder::pre_skip_48k() const {
    return valid() ? impl_->lookahead * (48'000 / impl_->config.sample_rate) : 0;
}

int64_t OpusPacketEncoder::input_frames() const { return impl_->input_frames; }

int64_t OpusPacketEncoder::encoded_frames() const { return impl_->encoded_frames; }

const OpusEncoderConfig& OpusPacketEncoder::config() const { return impl_->config; }

}  // namespace jkmeeting::audio
