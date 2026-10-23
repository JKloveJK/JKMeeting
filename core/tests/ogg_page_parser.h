#pragma once

#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <vector>

// 测试专用的 Ogg 解析器，按 RFC 3533 独立实现，不复用被测代码。
namespace jkmeeting::test {

struct OggPage {
    uint8_t header_type = 0;
    int64_t granule = 0;
    uint32_t serial = 0;
    uint32_t sequence = 0;
    uint32_t stored_crc = 0;
    uint32_t computed_crc = 0;
    std::vector<uint8_t> lacing;
    std::vector<std::vector<uint8_t>> packets;  // 本页内完整结束的包
    bool continued = false;
    bool bos() const { return (header_type & 0x02) != 0; }
    bool eos() const { return (header_type & 0x04) != 0; }
};

inline uint32_t ogg_crc_reference(const uint8_t* data, std::size_t size) {
    uint32_t crc = 0;
    for (std::size_t i = 0; i < size; ++i) {
        crc ^= static_cast<uint32_t>(data[i]) << 24;
        for (int bit = 0; bit < 8; ++bit) {
            crc = (crc & 0x80000000U) != 0 ? (crc << 1) ^ 0x04C11DB7U : crc << 1;
        }
    }
    return crc;
}

inline uint64_t read_le(const uint8_t* data, int bytes) {
    uint64_t value = 0;
    for (int i = bytes - 1; i >= 0; --i) {
        value = (value << 8) | data[i];
    }
    return value;
}

// 解析失败（魔数错误、长度不足、续包不完整）时返回 std::nullopt。
inline std::optional<std::vector<OggPage>> parse_ogg(const std::vector<uint8_t>& bytes) {
    std::vector<OggPage> pages;
    std::size_t offset = 0;
    while (offset < bytes.size()) {
        if (bytes.size() - offset < 27 || std::string(bytes.begin() + static_cast<long>(offset),
                                                      bytes.begin() + static_cast<long>(offset) + 4) != "OggS" ||
            bytes[offset + 4] != 0) {
            return std::nullopt;
        }
        const uint8_t* header = bytes.data() + offset;
        OggPage page;
        page.header_type = header[5];
        page.continued = (page.header_type & 0x01) != 0;
        page.granule = static_cast<int64_t>(read_le(header + 6, 8));
        page.serial = static_cast<uint32_t>(read_le(header + 14, 4));
        page.sequence = static_cast<uint32_t>(read_le(header + 18, 4));
        page.stored_crc = static_cast<uint32_t>(read_le(header + 22, 4));
        const std::size_t segments = header[26];
        if (bytes.size() - offset < 27 + segments) {
            return std::nullopt;
        }
        page.lacing.assign(header + 27, header + 27 + segments);
        std::size_t body = 0;
        for (const uint8_t value : page.lacing) {
            body += value;
        }
        const std::size_t page_size = 27 + segments + body;
        if (bytes.size() - offset < page_size) {
            return std::nullopt;
        }
        std::vector<uint8_t> copy(bytes.begin() + static_cast<long>(offset),
                                  bytes.begin() + static_cast<long>(offset + page_size));
        copy[22] = copy[23] = copy[24] = copy[25] = 0;
        page.computed_crc = ogg_crc_reference(copy.data(), copy.size());

        const uint8_t* data = header + 27 + segments;
        std::vector<uint8_t> current;
        for (const uint8_t value : page.lacing) {
            current.insert(current.end(), data, data + value);
            data += value;
            if (value < 255) {
                page.packets.push_back(current);
                current.clear();
            }
        }
        if (!current.empty() || (!page.lacing.empty() && page.lacing.back() == 255)) {
            return std::nullopt;  // 本项目的 muxer 不会产生跨页包
        }
        pages.push_back(std::move(page));
        offset += page_size;
    }
    return pages;
}

}  // namespace jkmeeting::test
