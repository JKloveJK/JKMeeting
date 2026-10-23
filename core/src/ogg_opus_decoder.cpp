#include "jkmeeting/ogg_opus_decoder.h"

#include <opus.h>

#include <algorithm>
#include <cstring>

namespace jkmeeting::audio {
namespace {

constexpr int kMaxFrames48k = 5'760;  // 120 ms，Opus 单包上限

bool supported_rate(int rate) {
    return rate == 8'000 || rate == 12'000 || rate == 16'000 || rate == 24'000 || rate == 48'000;
}

int64_t to_rate(int64_t frames_48k, int rate) { return frames_48k * rate / 48'000; }

}  // namespace

struct OggOpusDecoder::Impl {
    int requested_rate = 0;
    OggPacketReader reader;
    OpusDecoder* decoder = nullptr;
    bool failed = false;
    bool tags_seen = false;
    int channels = 0;
    int rate = 0;
    int pre_skip_48k = 0;
    int64_t decoded_frames = 0;  // 输出采样率下的解码帧数，含 pre-skip
    int64_t output_frames = 0;
    int64_t packets = 0;
    std::vector<int16_t> scratch;
    std::vector<OggPacket> pending;

    ~Impl() {
        if (decoder != nullptr) {
            opus_decoder_destroy(decoder);
        }
    }

    // RFC 7845 §5.1：OpusHead 须单独占据 BOS 页。
    bool parse_head(const OggPacket& packet) {
        const auto& data = packet.data;
        if (!packet.bos || !packet.last_on_page || data.size() < 19 || std::memcmp(data.data(), "OpusHead", 8) != 0 ||
            (data[8] & 0xF0) != 0 || data[18] != 0) {
            return false;
        }
        channels = data[9];
        pre_skip_48k = data[10] | (data[11] << 8);
        const int input_rate = static_cast<int>(data[12] | (data[13] << 8) | (data[14] << 16) |
                                                (static_cast<uint32_t>(data[15]) << 24));
        const auto gain_q8 = static_cast<int16_t>(data[16] | (data[17] << 8));
        if (channels < 1 || channels > 2) {
            return false;
        }
        if (requested_rate != 0) {
            rate = requested_rate;
        } else {
            rate = supported_rate(input_rate) ? input_rate : 48'000;
        }
        int error = OPUS_OK;
        decoder = opus_decoder_create(rate, channels, &error);
        if (error != OPUS_OK || decoder == nullptr) {
            decoder = nullptr;
            return false;
        }
        if (gain_q8 != 0 && opus_decoder_ctl(decoder, OPUS_SET_GAIN(gain_q8)) != OPUS_OK) {
            return false;
        }
        scratch.resize(static_cast<std::size_t>(kMaxFrames48k / (48'000 / rate) * channels));
        return true;
    }

    bool decode_audio(const OggPacket& packet, std::vector<int16_t>& out) {
        if (packet.data.empty()) {
            return false;  // 合法 Opus 包至少 1 字节（RFC 6716 §3.1）
        }
        const int max_frames = static_cast<int>(scratch.size()) / channels;
        const int frames = opus_decode(decoder, packet.data.data(), static_cast<opus_int32>(packet.data.size()),
                                       scratch.data(), max_frames, 0);
        if (frames < 0) {
            return false;
        }
        ++packets;
        const int64_t pre_skip = to_rate(pre_skip_48k, rate);
        int64_t begin = std::clamp<int64_t>(pre_skip - decoded_frames, 0, frames);
        int64_t end = frames;
        decoded_frames += frames;
        if (packet.eos) {
            if (packet.page_granule < pre_skip_48k) {
                return false;
            }
            const int64_t limit = to_rate(packet.page_granule - pre_skip_48k, rate);
            end = std::clamp<int64_t>(begin + (limit - output_frames), begin, frames);
            // EOS granule 声明的样本数多于实际解码数视为损坏（RFC 7845 §4.5）。
            if (packet.last_on_page && output_frames + (end - begin) < limit) {
                return false;
            }
        }
        out.insert(out.end(), scratch.begin() + static_cast<std::ptrdiff_t>(begin * channels),
                   scratch.begin() + static_cast<std::ptrdiff_t>(end * channels));
        output_frames += end - begin;
        return true;
    }

    bool handle(const OggPacket& packet, std::vector<int16_t>& out) {
        if (decoder == nullptr) {
            return parse_head(packet);
        }
        if (!tags_seen) {
            tags_seen = packet.data.size() >= 8 && std::memcmp(packet.data.data(), "OpusTags", 8) == 0;
            return tags_seen;
        }
        return decode_audio(packet, out);
    }
};

OggOpusDecoder::OggOpusDecoder(int output_sample_rate) : impl_(std::make_unique<Impl>()) {
    impl_->requested_rate = output_sample_rate;
    impl_->failed = output_sample_rate != 0 && !supported_rate(output_sample_rate);
}

OggOpusDecoder::~OggOpusDecoder() = default;

bool OggOpusDecoder::push(const uint8_t* data, std::size_t size, std::vector<int16_t>& out) {
    auto& impl = *impl_;
    if (impl.failed) {
        return false;
    }
    impl.pending.clear();
    const bool ok = impl.reader.push(data, size, impl.pending);
    for (const auto& packet : impl.pending) {
        if (!impl.handle(packet, out)) {
            impl.failed = true;
            return false;
        }
    }
    impl.failed = !ok;
    return ok;
}

bool OggOpusDecoder::finish() {
    return !impl_->failed && impl_->reader.complete() && impl_->packets > 0;
}

bool OggOpusDecoder::header_parsed() const { return impl_->decoder != nullptr; }

int OggOpusDecoder::sample_rate() const { return impl_->rate; }

int OggOpusDecoder::channels() const { return impl_->channels; }

int OggOpusDecoder::pre_skip_48k() const { return impl_->pre_skip_48k; }

int64_t OggOpusDecoder::output_frames() const { return impl_->output_frames; }

int64_t OggOpusDecoder::packet_count() const { return impl_->packets; }

}  // namespace jkmeeting::audio
