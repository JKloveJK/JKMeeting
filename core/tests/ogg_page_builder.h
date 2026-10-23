#pragma once

#include "ogg_page_parser.h"

#include <cstdint>
#include <vector>

// 测试专用的 Ogg 页构造器：可写出任意 lacing，用来构造被测封装器不会产生的合法或非法输入。
namespace jkmeeting::test {

inline void append_le(std::vector<uint8_t>& out, uint64_t value, int bytes) {
    for (int i = 0; i < bytes; ++i) {
        out.push_back(static_cast<uint8_t>(value >> (8 * i)));
    }
}

inline std::vector<uint8_t> build_page(uint8_t header_type, int64_t granule, uint32_t serial, uint32_t sequence,
                                       const std::vector<uint8_t>& lacing, const std::vector<uint8_t>& body) {
    std::vector<uint8_t> page = {'O', 'g', 'g', 'S', 0, header_type};
    append_le(page, static_cast<uint64_t>(granule), 8);
    append_le(page, serial, 4);
    append_le(page, sequence, 4);
    append_le(page, 0, 4);
    page.push_back(static_cast<uint8_t>(lacing.size()));
    page.insert(page.end(), lacing.begin(), lacing.end());
    page.insert(page.end(), body.begin(), body.end());
    const uint32_t crc = ogg_crc_reference(page.data(), page.size());
    for (int i = 0; i < 4; ++i) {
        page[22 + static_cast<std::size_t>(i)] = static_cast<uint8_t>(crc >> (8 * i));
    }
    return page;
}

// RFC 3533 的 lacing：floor(n / 255) 个 255，再加 n % 255（可能为 0）。
inline std::vector<uint8_t> lacing_for(std::size_t size) {
    std::vector<uint8_t> lacing(size / 255, 255);
    lacing.push_back(static_cast<uint8_t>(size % 255));
    return lacing;
}

inline std::vector<uint8_t> filled(std::size_t size, uint8_t seed) {
    std::vector<uint8_t> data(size);
    for (std::size_t i = 0; i < size; ++i) {
        data[i] = static_cast<uint8_t>(seed + i);
    }
    return data;
}

}  // namespace jkmeeting::test
