#include "jkmeeting/ogg_opus_muxer.h"

#include <opus.h>

#include <array>
#include <cstring>

namespace jkmeeting::audio {
namespace {

constexpr uint8_t kHeaderTypeBos = 0x02;
constexpr uint8_t kHeaderTypeEos = 0x04;
constexpr std::size_t kMaxPacketsPerPage = 50;
constexpr std::size_t kMaxLacingValues = 255;

std::array<uint32_t, 256> make_crc_table() {
    std::array<uint32_t, 256> table{};
    for (uint32_t index = 0; index < 256; ++index) {
        uint32_t crc = index << 24;
        for (int bit = 0; bit < 8; ++bit) {
            crc = (crc & 0x80000000U) != 0 ? (crc << 1) ^ 0x04C11DB7U : crc << 1;
        }
        table[index] = crc;
    }
    return table;
}

void put_le(std::vector<uint8_t>& out, uint64_t value, int bytes) {
    for (int i = 0; i < bytes; ++i) {
        out.push_back(static_cast<uint8_t>(value >> (8 * i)));
    }
}

void put_text(std::vector<uint8_t>& out, const char* text) {
    out.insert(out.end(), text, text + std::strlen(text));
}

std::vector<uint8_t> lacing_for(std::size_t size) {
    std::vector<uint8_t> lacing(size / 255, 255);
    lacing.push_back(static_cast<uint8_t>(size % 255));
    return lacing;
}

}  // namespace

uint32_t ogg_crc(const uint8_t* data, std::size_t size) {
    static const auto table = make_crc_table();
    uint32_t crc = 0;
    for (std::size_t i = 0; i < size; ++i) {
        crc = (crc << 8) ^ table[((crc >> 24) ^ data[i]) & 0xFFU];
    }
    return crc;
}

OggOpusMuxer::OggOpusMuxer(uint32_t serial, int channels, int input_sample_rate, int pre_skip_48k)
    : serial_(serial),
      channels_(channels),
      input_sample_rate_(input_sample_rate),
      pre_skip_48k_(pre_skip_48k),
      valid_(channels >= 1 && channels <= 2 && input_sample_rate > 0 && pre_skip_48k >= 0 &&
             pre_skip_48k <= 0xFFFF) {}

std::vector<uint8_t> OggOpusMuxer::headers() {
    std::vector<uint8_t> out;
    if (!valid_ || headers_written_) {
        return out;
    }
    headers_written_ = true;

    std::vector<uint8_t> head;
    put_text(head, "OpusHead");
    head.push_back(1);
    head.push_back(static_cast<uint8_t>(channels_));
    put_le(head, static_cast<uint64_t>(pre_skip_48k_), 2);
    put_le(head, static_cast<uint64_t>(input_sample_rate_), 4);
    put_le(head, 0, 2);
    head.push_back(0);
    write_page(kHeaderTypeBos, 0, lacing_for(head.size()), head, out);

    const char* vendor = opus_get_version_string();
    std::vector<uint8_t> tags;
    put_text(tags, "OpusTags");
    put_le(tags, std::strlen(vendor), 4);
    put_text(tags, vendor);
    put_le(tags, 0, 4);
    write_page(0, 0, lacing_for(tags.size()), tags, out);
    return out;
}

bool OggOpusMuxer::append(const EncodedPacket& packet, int64_t granule_end_48k, std::vector<uint8_t>& out) {
    if (!headers_written_ || finished_ || packet.data.empty() || packet.frames <= 0 ||
        granule_end_48k <= page_granule_) {
        return false;
    }
    const auto lacing = lacing_for(packet.data.size());
    if (lacing.size() > kMaxLacingValues) {
        return false;
    }
    if (page_packets_ == kMaxPacketsPerPage || lacing_.size() + lacing.size() > kMaxLacingValues) {
        seal_current_page(0, page_granule_, out);
    }
    lacing_.insert(lacing_.end(), lacing.begin(), lacing.end());
    body_.insert(body_.end(), packet.data.begin(), packet.data.end());
    ++page_packets_;
    ++audio_packets_;
    page_granule_ = granule_end_48k;
    return true;
}

bool OggOpusMuxer::finish(int64_t final_granule_48k, std::vector<uint8_t>& out) {
    if (!headers_written_ || finished_ || audio_packets_ == 0 || final_granule_48k < last_sealed_granule_ ||
        final_granule_48k > page_granule_) {
        return false;
    }
    seal_current_page(kHeaderTypeEos, final_granule_48k, out);
    finished_ = true;
    return true;
}

void OggOpusMuxer::seal_current_page(uint8_t header_type, int64_t granule, std::vector<uint8_t>& out) {
    write_page(header_type, granule, lacing_, body_, out);
    last_sealed_granule_ = granule;
    lacing_.clear();
    body_.clear();
    page_packets_ = 0;
}

void OggOpusMuxer::write_page(uint8_t header_type, int64_t granule, const std::vector<uint8_t>& lacing,
                              const std::vector<uint8_t>& body, std::vector<uint8_t>& out) {
    const std::size_t start = out.size();
    put_text(out, "OggS");
    out.push_back(0);
    out.push_back(header_type);
    put_le(out, static_cast<uint64_t>(granule), 8);
    put_le(out, serial_, 4);
    put_le(out, sequence_++, 4);
    const std::size_t crc_offset = out.size();
    put_le(out, 0, 4);
    out.push_back(static_cast<uint8_t>(lacing.size()));
    out.insert(out.end(), lacing.begin(), lacing.end());
    out.insert(out.end(), body.begin(), body.end());

    const uint32_t crc = ogg_crc(out.data() + start, out.size() - start);
    for (int i = 0; i < 4; ++i) {
        out[crc_offset + static_cast<std::size_t>(i)] = static_cast<uint8_t>(crc >> (8 * i));
    }
}

}  // namespace jkmeeting::audio
