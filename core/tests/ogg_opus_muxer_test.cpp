#include "jkmeeting/ogg_opus_muxer.h"

#include "ogg_page_parser.h"

#include <cassert>
#include <cstdint>
#include <cstring>
#include <string>
#include <vector>

namespace {

using jkmeeting::audio::EncodedPacket;
using jkmeeting::audio::OggOpusMuxer;
using jkmeeting::audio::ogg_crc;
using jkmeeting::test::OggPage;
using jkmeeting::test::parse_ogg;
using jkmeeting::test::read_le;

constexpr uint32_t kSerial = 0x1234ABCDU;
constexpr int kRate = 16'000;
constexpr int kPreSkip = 312;
constexpr int64_t kFrame = 320;           // 20 ms @ 16 kHz
constexpr int64_t kFrame48k = kFrame * 3;

EncodedPacket packet_of(std::size_t size, uint8_t seed) {
    EncodedPacket packet;
    packet.data.resize(size);
    for (std::size_t i = 0; i < size; ++i) {
        packet.data[i] = static_cast<uint8_t>(seed + i);
    }
    packet.frames = kFrame;
    return packet;
}

struct MuxResult {
    std::vector<uint8_t> bytes;
    std::vector<OggPage> pages;
};

MuxResult mux(const std::vector<EncodedPacket>& packets, int64_t final_granule, int channels = 1) {
    OggOpusMuxer muxer(kSerial, channels, kRate, kPreSkip);
    MuxResult result;
    result.bytes = muxer.headers();
    int64_t granule = 0;
    for (const auto& packet : packets) {
        granule += packet.frames * 3;
        std::vector<uint8_t> out;
        assert(muxer.append(packet, granule, out));
        result.bytes.insert(result.bytes.end(), out.begin(), out.end());
    }
    std::vector<uint8_t> tail;
    assert(muxer.finish(final_granule, tail));
    result.bytes.insert(result.bytes.end(), tail.begin(), tail.end());
    auto parsed = parse_ogg(result.bytes);
    assert(parsed.has_value());
    result.pages = std::move(*parsed);
    return result;
}

void check_stream_invariants(const MuxResult& result, std::size_t expected_packets, int64_t final_granule) {
    const auto& pages = result.pages;
    assert(pages.size() >= 3);
    std::size_t audio_packets = 0;
    int64_t previous_granule = 0;
    for (std::size_t i = 0; i < pages.size(); ++i) {
        const auto& page = pages[i];
        assert(page.serial == kSerial);
        assert(page.sequence == i);
        assert(page.stored_crc == page.computed_crc);
        assert(page.bos() == (i == 0));
        assert(page.eos() == (i + 1 == pages.size()));
        assert(!page.continued);
        assert(!page.packets.empty());  // 不允许空页，包括 EOS 页
        if (i >= 2) {
            assert(page.granule >= previous_granule);
            previous_granule = page.granule;
            audio_packets += page.packets.size();
            assert(page.packets.size() <= 50);
        }
    }
    assert(audio_packets == expected_packets);
    assert(pages.back().granule == final_granule);
}

void test_crc_known_value() {
    const char* text = "123456789";
    assert(ogg_crc(reinterpret_cast<const uint8_t*>(text), std::strlen(text)) == 0x89A1897FU);
}

void test_headers_layout_and_once_only() {
    OggOpusMuxer muxer(kSerial, 2, 48'000, kPreSkip);
    const auto bytes = muxer.headers();
    assert(muxer.headers().empty());

    const auto pages = parse_ogg(bytes);
    assert(pages.has_value() && pages->size() == 2);
    const auto& head_page = (*pages)[0];
    const auto& tags_page = (*pages)[1];
    assert(head_page.bos() && !tags_page.bos());
    assert(head_page.granule == 0 && tags_page.granule == 0);
    assert(head_page.packets.size() == 1 && tags_page.packets.size() == 1);

    const auto& head = head_page.packets[0];
    assert(head.size() == 19);
    assert(std::string(head.begin(), head.begin() + 8) == "OpusHead");
    assert(head[8] == 1);
    assert(head[9] == 2);
    assert(read_le(head.data() + 10, 2) == static_cast<uint64_t>(kPreSkip));
    assert(read_le(head.data() + 12, 4) == 48'000U);
    assert(read_le(head.data() + 16, 2) == 0);
    assert(head[18] == 0);

    const auto& tags = tags_page.packets[0];
    assert(std::string(tags.begin(), tags.begin() + 8) == "OpusTags");
    const auto vendor_length = read_le(tags.data() + 8, 4);
    assert(vendor_length > 0);
    assert(tags.size() == 8 + 4 + vendor_length + 4);
    assert(read_le(tags.data() + 12 + vendor_length, 4) == 0);
}

void test_lacing_preserves_packet_bytes() {
    const std::vector<std::size_t> sizes = {255, 510, 600, 1275, 1};
    std::vector<EncodedPacket> packets;
    for (std::size_t i = 0; i < sizes.size(); ++i) {
        packets.push_back(packet_of(sizes[i], static_cast<uint8_t>(i * 7)));
    }
    const int64_t final_granule = static_cast<int64_t>(sizes.size()) * kFrame48k - 100;
    const auto result = mux(packets, final_granule);
    check_stream_invariants(result, sizes.size(), final_granule);

    const auto& audio = result.pages[2];
    assert(audio.packets.size() == sizes.size());
    for (std::size_t i = 0; i < sizes.size(); ++i) {
        assert(audio.packets[i] == packets[i].data);
    }
    // 255 -> [255, 0]; 510 -> [255, 255, 0]; 600 -> [255, 255, 90]; 1275 -> 5x255 + 0; 1 -> [1]
    const std::vector<uint8_t> expected = {255, 0, 255, 255, 0, 255, 255, 90,
                                           255, 255, 255, 255, 255, 0, 1};
    assert(audio.lacing == expected);
}

void test_eos_carries_last_packet_at_page_boundaries() {
    for (const std::size_t count : {std::size_t{1}, std::size_t{50}, std::size_t{51}, std::size_t{150}}) {
        std::vector<EncodedPacket> packets;
        for (std::size_t i = 0; i < count; ++i) {
            packets.push_back(packet_of(60, static_cast<uint8_t>(i)));
        }
        const int64_t total = static_cast<int64_t>(count) * kFrame48k;
        const int64_t final_granule = total - 200;
        const auto result = mux(packets, final_granule);
        check_stream_invariants(result, count, final_granule);
        assert(result.pages.size() == 2 + (count + 49) / 50);
        // 非末页的 granule 是累计解码样本数，不额外加 pre_skip。
        if (result.pages.size() > 3) {
            assert(result.pages[2].granule == 50 * kFrame48k);
        }
    }
}

void test_lacing_limit_splits_pages() {
    // 每个 1275 字节的包占 6 个 lacing 值，255 / 6 = 42 个包后必须换页。
    std::vector<EncodedPacket> packets;
    for (int i = 0; i < 43; ++i) {
        packets.push_back(packet_of(1275, static_cast<uint8_t>(i)));
    }
    const int64_t final_granule = 43 * kFrame48k - 10;
    const auto result = mux(packets, final_granule);
    check_stream_invariants(result, 43, final_granule);
    assert(result.pages.size() == 4);
    assert(result.pages[2].packets.size() == 42);
    assert(result.pages[2].lacing.size() == 252);
}

void test_zero_packets_and_invalid_usage() {
    OggOpusMuxer muxer(kSerial, 1, kRate, kPreSkip);
    std::vector<uint8_t> out;
    assert(!muxer.append(packet_of(10, 0), kFrame48k, out));  // headers 之前
    (void)muxer.headers();
    assert(!muxer.finish(0, out));  // 没有任何音频包
    assert(out.empty());

    OggOpusMuxer second(kSerial, 1, kRate, kPreSkip);
    (void)second.headers();
    assert(!second.append(EncodedPacket{}, kFrame48k, out));              // 空包
    assert(second.append(packet_of(10, 0), kFrame48k, out));
    assert(!second.append(packet_of(10, 0), kFrame48k - 1, out));         // granule 倒退
    assert(!second.finish(kFrame48k + 1, out));                           // 超过最后一个包的结束位置
    assert(second.finish(kFrame48k, out));
    assert(!second.append(packet_of(10, 0), 2 * kFrame48k, out));         // finish 之后
    assert(!second.finish(kFrame48k, out));
}

void test_final_granule_not_before_previous_page() {
    OggOpusMuxer muxer(kSerial, 1, kRate, kPreSkip);
    (void)muxer.headers();
    std::vector<uint8_t> out;
    for (int i = 1; i <= 51; ++i) {
        assert(muxer.append(packet_of(10, 0), i * kFrame48k, out));
    }
    assert(!muxer.finish(50 * kFrame48k - 1, out));
}

void test_stereo_granule_counts_per_channel() {
    std::vector<EncodedPacket> packets = {packet_of(40, 1), packet_of(40, 2)};
    const int64_t final_granule = 2 * kFrame48k - 50;
    const auto result = mux(packets, final_granule, 2);
    check_stream_invariants(result, 2, final_granule);
    assert(result.pages[0].packets[0][9] == 2);
}

}  // namespace

int main() {
    test_crc_known_value();
    test_headers_layout_and_once_only();
    test_lacing_preserves_packet_bytes();
    test_eos_carries_last_packet_at_page_boundaries();
    test_lacing_limit_splits_pages();
    test_zero_packets_and_invalid_usage();
    test_final_granule_not_before_previous_page();
    test_stereo_granule_counts_per_channel();
    return 0;
}
