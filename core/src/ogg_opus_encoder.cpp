#include "jkmeeting/ogg_opus_encoder.h"

namespace jkmeeting::audio {
namespace {

int scale_to_48k(const OpusEncoderConfig& config) { return 48'000 / config.sample_rate; }

}  // namespace

OggOpusEncoder::OggOpusEncoder(const OpusEncoderConfig& config, uint32_t serial)
    : encoder_(config), muxer_(serial, config.channels, config.sample_rate, encoder_.pre_skip_48k()) {}

bool OggOpusEncoder::valid() const { return encoder_.valid() && muxer_.valid(); }

std::vector<uint8_t> OggOpusEncoder::headers() {
    if (!valid() || headers_written_) {
        return {};
    }
    headers_written_ = true;
    auto bytes = muxer_.headers();
    bytes_written_ += static_cast<int64_t>(bytes.size());
    return bytes;
}

bool OggOpusEncoder::encode(const int16_t* samples, std::size_t sample_count, std::vector<uint8_t>& out) {
    if (!headers_written_ || failed_) {
        return false;
    }
    packets_.clear();
    if (!encoder_.encode(samples, sample_count, packets_)) {
        failed_ = true;
        return false;
    }
    return mux(packets_, out);
}

bool OggOpusEncoder::finish(std::vector<uint8_t>& out) {
    if (!headers_written_ || failed_) {
        return false;
    }
    failed_ = true;  // finish 之后不再接受任何调用
    packets_.clear();
    if (!encoder_.flush(packets_) || !mux(packets_, out)) {
        return false;
    }
    const int64_t final_granule =
        encoder_.pre_skip_48k() + encoder_.input_frames() * scale_to_48k(encoder_.config());
    const std::size_t before = out.size();
    if (!muxer_.finish(final_granule, out)) {
        return false;
    }
    bytes_written_ += static_cast<int64_t>(out.size() - before);
    return true;
}

int64_t OggOpusEncoder::duration_ms() const {
    return encoder_.input_frames() * 1'000 / encoder_.config().sample_rate;
}

bool OggOpusEncoder::mux(std::vector<EncodedPacket>& packets, std::vector<uint8_t>& out) {
    const std::size_t before = out.size();
    const int scale = scale_to_48k(encoder_.config());
    for (const auto& packet : packets) {
        granule_ += packet.frames * scale;
        if (!muxer_.append(packet, granule_, out)) {
            failed_ = true;
            return false;
        }
        ++packet_count_;
    }
    bytes_written_ += static_cast<int64_t>(out.size() - before);
    return true;
}

}  // namespace jkmeeting::audio
