#include "jkmeeting/ogg_packet_reader.h"

#include "ogg_page_builder.h"

#include <cassert>
#include <cstdint>
#include <vector>

namespace {

using jkmeeting::audio::OggPacket;
using jkmeeting::audio::OggPacketReader;
using jkmeeting::test::build_page;
using jkmeeting::test::filled;
using jkmeeting::test::lacing_for;

constexpr uint32_t kSerial = 42U;

std::vector<uint8_t> concat(const std::vector<std::vector<uint8_t>>& parts) {
    std::vector<uint8_t> out;
    for (const auto& part : parts) {
        out.insert(out.end(), part.begin(), part.end());
    }
    return out;
}

std::vector<OggPacket> read_all(const std::vector<uint8_t>& bytes, std::size_t chunk, OggPacketReader& reader) {
    std::vector<OggPacket> packets;
    for (std::size_t offset = 0; offset < bytes.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, bytes.size() - offset);
        if (!reader.push(bytes.data() + offset, count, packets)) {
            break;
        }
    }
    return packets;
}

// 页 0：BOS，一个 3 字节包；页 1：两个包，其中 510 字节的包 lacing 为 255,255,0；
// 页 2：600 字节包的前 510 字节（lacing 以 255 结尾，跨页）；页 3：续包剩余 90 字节 + 0 长度包，EOS。
std::vector<uint8_t> crafted_stream(const std::vector<uint8_t>& big, const std::vector<uint8_t>& exact) {
    const auto first = filled(3, 1);
    const auto small = filled(10, 9);
    std::vector<uint8_t> lacing1 = lacing_for(exact.size());
    lacing1.push_back(static_cast<uint8_t>(small.size()));
    std::vector<uint8_t> body1 = exact;
    body1.insert(body1.end(), small.begin(), small.end());
    const std::vector<uint8_t> body2(big.begin(), big.begin() + 510);
    const std::vector<uint8_t> body3(big.begin() + 510, big.end());
    return concat({
        build_page(0x02, 0, kSerial, 0, lacing_for(first.size()), first),
        build_page(0x00, 1'000, kSerial, 1, lacing1, body1),
        build_page(0x00, -1, kSerial, 2, {255, 255}, body2),
        build_page(0x05, 2'000, kSerial, 3, {90, 0}, body3),
    });
}

void test_crafted_stream_with_continuation_and_zero_lacing() {
    const auto big = filled(600, 50);
    const auto exact = filled(510, 77);
    const auto bytes = crafted_stream(big, exact);

    OggPacketReader reader;
    const auto packets = read_all(bytes, bytes.size(), reader);
    assert(!reader.failed());
    assert(reader.complete());
    assert(reader.pages() == 4);
    assert(packets.size() == 5);
    assert(packets[0].bos && packets[0].data.size() == 3 && packets[0].last_on_page);
    assert(packets[1].data == exact && !packets[1].last_on_page && packets[1].page_granule == 1'000);
    assert(packets[2].data.size() == 10 && packets[2].last_on_page);
    assert(packets[3].data == big && packets[3].eos && !packets[3].last_on_page);
    assert(packets[4].data.empty() && packets[4].eos && packets[4].last_on_page && packets[4].page_granule == 2'000);
}

void test_chunking_does_not_change_output() {
    const auto bytes = crafted_stream(filled(600, 50), filled(510, 77));
    OggPacketReader whole;
    const auto expected = read_all(bytes, bytes.size(), whole);
    for (const std::size_t chunk : {1U, 7U, 27U, 300U}) {
        OggPacketReader reader;
        const auto packets = read_all(bytes, chunk, reader);
        assert(reader.complete());
        assert(packets.size() == expected.size());
        for (std::size_t i = 0; i < packets.size(); ++i) {
            assert(packets[i].data == expected[i].data);
            assert(packets[i].page_granule == expected[i].page_granule);
            assert(packets[i].last_on_page == expected[i].last_on_page);
        }
    }
}

void test_rejects_corrupted_crc() {
    auto bytes = crafted_stream(filled(600, 50), filled(510, 77));
    bytes[40] ^= 0x01;  // 落在页 1 内
    OggPacketReader reader;
    std::vector<OggPacket> packets;
    assert(!reader.push(bytes.data(), bytes.size(), packets));
    assert(reader.failed());
    assert(packets.size() == 1);  // 只有页 0 的包被输出
    assert(!reader.push(bytes.data(), 1, packets));
}

void test_rejects_bad_magic_and_version() {
    auto bad_magic = build_page(0x02, 0, kSerial, 0, {1}, {9});
    bad_magic[0] = 'X';
    OggPacketReader magic_reader;
    std::vector<OggPacket> packets;
    assert(!magic_reader.push(bad_magic.data(), bad_magic.size(), packets));

    auto bad_version = build_page(0x02, 0, kSerial, 0, {1}, {9});
    bad_version[4] = 1;
    OggPacketReader version_reader;
    assert(!version_reader.push(bad_version.data(), bad_version.size(), packets));
}

void test_rejects_continuation_mismatch_and_second_serial() {
    std::vector<OggPacket> packets;
    // 首页声明续包，但前面没有未结束的包。
    const auto orphan = build_page(0x03, 0, kSerial, 0, {1}, {9});
    OggPacketReader orphan_reader;
    assert(!orphan_reader.push(orphan.data(), orphan.size(), packets));

    // 有未结束的包，下一页却没有续包标志。
    const auto dangling = concat({build_page(0x02, -1, kSerial, 0, {255}, filled(255, 1)),
                                  build_page(0x04, 10, kSerial, 1, {1}, {7})});
    OggPacketReader dangling_reader;
    assert(!dangling_reader.push(dangling.data(), dangling.size(), packets));

    const auto multiplexed =
        concat({build_page(0x02, 0, kSerial, 0, {1}, {9}), build_page(0x02, 0, kSerial + 1, 0, {1}, {9})});
    OggPacketReader multiplexed_reader;
    assert(!multiplexed_reader.push(multiplexed.data(), multiplexed.size(), packets));

    const auto skipped =
        concat({build_page(0x02, 0, kSerial, 0, {1}, {9}), build_page(0x04, 0, kSerial, 2, {1}, {9})});
    OggPacketReader skipped_reader;
    assert(!skipped_reader.push(skipped.data(), skipped.size(), packets));
}

void test_truncated_stream_is_incomplete() {
    const auto bytes = crafted_stream(filled(600, 50), filled(510, 77));
    OggPacketReader reader;
    std::vector<OggPacket> packets;
    assert(reader.push(bytes.data(), bytes.size() - 5, packets));
    assert(!reader.failed());
    assert(!reader.complete());
}

}  // namespace

int main() {
    test_crafted_stream_with_continuation_and_zero_lacing();
    test_chunking_does_not_change_output();
    test_rejects_corrupted_crc();
    test_rejects_bad_magic_and_version();
    test_rejects_continuation_mismatch_and_second_serial();
    test_truncated_stream_is_incomplete();
    return 0;
}
