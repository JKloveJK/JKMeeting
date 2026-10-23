#include "jkmeeting/ogg_opus_encoder.h"

#include "ogg_page_parser.h"
#include "test_signals.h"

#include <opus.h>

#include <algorithm>
#include <cassert>
#include <cstdint>
#include <vector>

namespace {

using jkmeeting::audio::OggOpusEncoder;
using jkmeeting::audio::OpusEncoderConfig;
using jkmeeting::test::parse_ogg;
using jkmeeting::test::read_le;
using jkmeeting::test::sine;

constexpr int kRate = 16'000;
constexpr uint32_t kSerial = 7U;

std::vector<uint8_t> encode_stream(const std::vector<int16_t>& pcm, std::size_t chunk, OggOpusEncoder& encoder) {
    std::vector<uint8_t> bytes = encoder.headers();
    for (std::size_t offset = 0; offset < pcm.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, pcm.size() - offset);
        assert(encoder.encode(pcm.data() + offset, count, bytes));
    }
    assert(encoder.finish(bytes));
    return bytes;
}

void test_stream_structure_and_final_granule() {
    const auto pcm = sine(440.0, kRate, kRate * 3, 8'000.0);  // 3 秒，正好 150 个包
    OggOpusEncoder encoder(OpusEncoderConfig{}, kSerial);
    assert(encoder.valid());
    const auto bytes = encode_stream(pcm, 512, encoder);
    assert(encoder.bytes_written() == static_cast<int64_t>(bytes.size()));
    assert(encoder.duration_ms() == 3'000);

    const auto pages = parse_ogg(bytes);
    assert(pages.has_value());
    const auto pre_skip = static_cast<int64_t>(read_le((*pages)[0].packets[0].data() + 10, 2));
    assert(pre_skip > 0);
    std::size_t packets = 0;
    for (std::size_t i = 2; i < pages->size(); ++i) {
        assert((*pages)[i].stored_crc == (*pages)[i].computed_crc);
        packets += (*pages)[i].packets.size();
    }
    assert(static_cast<int64_t>(packets) == encoder.packet_count());
    assert(packets == 151);  // 150 个满帧 + 1 个覆盖 lookahead 的补零帧
    assert(pages->back().eos());
    assert(pages->back().granule == pre_skip + static_cast<int64_t>(pcm.size()) * 3);
}

void test_decoded_length_matches_input_after_trimming() {
    const auto pcm = sine(440.0, kRate, kRate + 123, 8'000.0);
    OggOpusEncoder encoder(OpusEncoderConfig{}, kSerial);
    const auto bytes = encode_stream(pcm, 1'000, encoder);
    const auto pages = parse_ogg(bytes);
    assert(pages.has_value());

    int error = 0;
    OpusDecoder* decoder = opus_decoder_create(48'000, 1, &error);
    assert(error == OPUS_OK);
    std::vector<int16_t> buffer(5'760);
    int64_t decoded = 0;
    for (std::size_t i = 2; i < pages->size(); ++i) {
        for (const auto& packet : (*pages)[i].packets) {
            decoded += opus_decode(decoder, packet.data(), static_cast<opus_int32>(packet.size()), buffer.data(),
                                   5'760, 0);
        }
    }
    opus_decoder_destroy(decoder);
    const auto pre_skip = static_cast<int64_t>(read_le((*pages)[0].packets[0].data() + 10, 2));
    const int64_t playable = pages->back().granule - pre_skip;
    assert(playable == static_cast<int64_t>(pcm.size()) * 3);
    assert(decoded >= pages->back().granule);
}

void test_chunking_is_byte_identical() {
    const auto pcm = sine(300.0, kRate, kRate * 2 + 77, 6'000.0);
    OggOpusEncoder small(OpusEncoderConfig{}, kSerial);
    OggOpusEncoder large(OpusEncoderConfig{}, kSerial);
    assert(encode_stream(pcm, 37, small) == encode_stream(pcm, 9'999, large));
}

void test_invalid_usage() {
    OpusEncoderConfig bad;
    bad.sample_rate = 44'100;
    OggOpusEncoder invalid(bad, kSerial);
    assert(!invalid.valid());
    assert(invalid.headers().empty());

    OggOpusEncoder empty(OpusEncoderConfig{}, kSerial);
    std::vector<uint8_t> out = empty.headers();
    assert(!out.empty());
    assert(!empty.finish(out));  // 零长度录音不是有效文件

    OggOpusEncoder no_headers(OpusEncoderConfig{}, kSerial);
    const auto pcm = sine(440.0, kRate, 640, 8'000.0);
    std::vector<uint8_t> bytes;
    assert(!no_headers.encode(pcm.data(), pcm.size(), bytes));
}

}  // namespace

int main() {
    test_stream_structure_and_final_granule();
    test_decoded_length_matches_input_after_trimming();
    test_chunking_is_byte_identical();
    test_invalid_usage();
    return 0;
}
