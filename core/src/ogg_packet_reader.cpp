#include "jkmeeting/ogg_packet_reader.h"

#include "jkmeeting/ogg_opus_muxer.h"

#include <algorithm>
#include <cstring>

namespace jkmeeting::audio {
namespace {

constexpr std::size_t kHeaderBytes = 27;

uint64_t read_le(const uint8_t* data, int bytes) {
    uint64_t value = 0;
    for (int i = bytes - 1; i >= 0; --i) {
        value = (value << 8) | data[i];
    }
    return value;
}

}  // namespace

bool OggPacketReader::push(const uint8_t* data, std::size_t size, std::vector<OggPacket>& out) {
    if (failed_ || (data == nullptr && size > 0)) {
        failed_ = true;
        return false;
    }
    buffer_.insert(buffer_.end(), data, data + size);
    std::size_t offset = 0;
    while (buffer_.size() - offset >= kHeaderBytes) {
        const uint8_t* page = buffer_.data() + offset;
        if (std::memcmp(page, "OggS", 4) != 0 || page[4] != 0) {
            failed_ = true;
            return false;
        }
        const std::size_t segments = page[26];
        if (buffer_.size() - offset < kHeaderBytes + segments) {
            break;
        }
        std::size_t body = 0;
        for (std::size_t i = 0; i < segments; ++i) {
            body += page[kHeaderBytes + i];
        }
        const std::size_t header_size = kHeaderBytes + segments;
        if (buffer_.size() - offset < header_size + body) {
            break;
        }
        if (!parse_page(page, header_size, body, out)) {
            failed_ = true;
            return false;
        }
        offset += header_size + body;
    }
    buffer_.erase(buffer_.begin(), buffer_.begin() + static_cast<std::ptrdiff_t>(offset));
    return true;
}

bool OggPacketReader::parse_page(const uint8_t* page, std::size_t header_size, std::size_t body_size,
                                 std::vector<OggPacket>& out) {
    std::vector<uint8_t> copy(page, page + header_size + body_size);
    std::fill(copy.begin() + 22, copy.begin() + 26, uint8_t{0});
    if (ogg_crc(copy.data(), copy.size()) != static_cast<uint32_t>(read_le(page + 22, 4))) {
        return false;
    }

    const uint8_t header_type = page[5];
    const bool continued = (header_type & 0x01) != 0;
    const bool bos = (header_type & 0x02) != 0;
    const bool eos = (header_type & 0x04) != 0;
    const auto granule = static_cast<int64_t>(read_le(page + 6, 8));
    const auto serial = static_cast<uint32_t>(read_le(page + 14, 4));
    const auto sequence = static_cast<uint32_t>(read_le(page + 18, 4));

    if (eos_seen_ || bos != !serial_known_ || (serial_known_ && serial != serial_) || sequence != next_sequence_ ||
        continued != has_partial_) {
        return false;
    }
    serial_known_ = true;
    serial_ = serial;

    const std::size_t first_index = out.size();
    const uint8_t* body = page + header_size;
    for (std::size_t i = kHeaderBytes; i < header_size; ++i) {
        const uint8_t value = page[i];
        partial_packet_.insert(partial_packet_.end(), body, body + value);
        body += value;
        if (value == 255) {
            has_partial_ = true;
            continue;
        }
        OggPacket packet;
        packet.data = std::move(partial_packet_);
        packet.page_granule = granule;
        packet.bos = bos;
        packet.eos = eos;
        out.push_back(std::move(packet));
        partial_packet_.clear();
        has_partial_ = false;
    }
    if (out.size() > first_index) {
        out.back().last_on_page = true;
    }
    if (eos && has_partial_) {
        return false;
    }
    eos_seen_ = eos;
    ++next_sequence_;
    ++pages_;
    return true;
}

bool OggPacketReader::complete() const { return !failed_ && eos_seen_ && buffer_.empty() && !has_partial_; }

}  // namespace jkmeeting::audio
