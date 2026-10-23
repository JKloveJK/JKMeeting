#include "jkmeeting/ogg_opus_decoder.h"
#include "jkmeeting/ogg_opus_encoder.h"

#include "ogg_page_builder.h"
#include "test_signals.h"

#include <algorithm>
#include <cassert>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <vector>

namespace {

using jkmeeting::audio::OggOpusDecoder;
using jkmeeting::audio::OggOpusEncoder;
using jkmeeting::audio::OpusEncoderConfig;
using jkmeeting::test::build_page;
using jkmeeting::test::speech_band;

constexpr int kRate = 16'000;
constexpr uint32_t kSerial = 11U;

std::vector<uint8_t> encode(const std::vector<int16_t>& pcm, const OpusEncoderConfig& config = OpusEncoderConfig{}) {
    OggOpusEncoder encoder(config, kSerial);
    assert(encoder.valid());
    std::vector<uint8_t> bytes = encoder.headers();
    assert(encoder.encode(pcm.data(), pcm.size(), bytes));
    assert(encoder.finish(bytes));
    return bytes;
}

std::vector<int16_t> decode(const std::vector<uint8_t>& bytes, std::size_t chunk, OggOpusDecoder& decoder) {
    std::vector<int16_t> pcm;
    for (std::size_t offset = 0; offset < bytes.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, bytes.size() - offset);
        assert(decoder.push(bytes.data() + offset, count, pcm));
    }
    assert(decoder.finish());
    return pcm;
}

void test_round_trip_length_and_header() {
    const auto input = speech_band(kRate, kRate * 2 + 123);
    const auto bytes = encode(input);
    OggOpusDecoder decoder;
    const auto output = decode(bytes, bytes.size(), decoder);
    assert(decoder.header_parsed());
    assert(decoder.sample_rate() == kRate);
    assert(decoder.channels() == 1);
    assert(decoder.pre_skip_48k() == 312);
    assert(output.size() == input.size());
    assert(decoder.output_frames() == static_cast<int64_t>(input.size()));
    assert(decoder.packet_count() == 101);  // 32123 帧 -> 100 个满帧 + 1 个含尾部与 lookahead 的补零帧

    // 裁掉 pre_skip 后无需再对齐；跳过前 100 ms 编码器收敛期。
    double signal = 0.0;
    double noise = 0.0;
    for (std::size_t i = kRate / 10; i < input.size(); ++i) {
        const double diff = static_cast<double>(input[i]) - output[i];
        signal += static_cast<double>(input[i]) * input[i];
        noise += diff * diff;
    }
    const double snr_db = 10.0 * std::log10(signal / noise);
    std::printf("{\"case\":\"ogg_opus_round_trip\",\"snr_db\":%.2f}\n", snr_db);
    assert(snr_db >= 12.0);  // 回归护栏，与 P2-2 B2 一致
}

void test_chunking_does_not_change_output() {
    const auto bytes = encode(speech_band(kRate, kRate + 77));
    OggOpusDecoder whole;
    const auto expected = decode(bytes, bytes.size(), whole);
    for (const std::size_t chunk : {1U, 13U, 4'096U}) {
        OggOpusDecoder decoder;
        assert(decode(bytes, chunk, decoder) == expected);
    }
}

void test_explicit_48k_output_and_stereo() {
    const auto mono = speech_band(kRate, kRate);
    OggOpusDecoder upsampled(48'000);
    const auto bytes = encode(mono);
    assert(decode(bytes, 999, upsampled).size() == mono.size() * 3);
    assert(upsampled.sample_rate() == 48'000);

    OpusEncoderConfig stereo_config;
    stereo_config.channels = 2;
    std::vector<int16_t> stereo(static_cast<std::size_t>(kRate) * 2);
    for (std::size_t i = 0; i < mono.size(); ++i) {
        stereo[2 * i] = mono[i];
        stereo[2 * i + 1] = static_cast<int16_t>(mono[i] / 2);
    }
    OggOpusDecoder stereo_decoder;
    const auto output = decode(encode(stereo, stereo_config), 512, stereo_decoder);
    assert(stereo_decoder.channels() == 2);
    assert(output.size() == stereo.size());
    assert(stereo_decoder.output_frames() == kRate);
}

void test_rejects_non_opus_and_bad_header() {
    std::vector<int16_t> pcm;
    const std::vector<uint8_t> vorbis = {1, 'v', 'o', 'r', 'b', 'i', 's', 0, 0, 0, 0, 1, 0x44, 0xAC, 0, 0, 0, 0, 0};
    const auto vorbis_page = build_page(0x02, 0, kSerial, 0, {static_cast<uint8_t>(vorbis.size())}, vorbis);
    OggOpusDecoder vorbis_decoder;
    assert(!vorbis_decoder.push(vorbis_page.data(), vorbis_page.size(), pcm));

    // OpusHead 声明映射族 1（多声道）——本解码器只支持映射族 0。
    std::vector<uint8_t> head = {'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, 1, 0x38, 0x01, 0x80, 0x3E, 0, 0, 0, 0, 1};
    const auto head_page = build_page(0x02, 0, kSerial, 0, {static_cast<uint8_t>(head.size())}, head);
    OggOpusDecoder family_decoder;
    assert(!family_decoder.push(head_page.data(), head_page.size(), pcm));
}

void test_truncated_stream_fails_on_finish() {
    const auto bytes = encode(speech_band(kRate, kRate));
    OggOpusDecoder decoder;
    std::vector<int16_t> pcm;
    assert(decoder.push(bytes.data(), bytes.size() - 20, pcm));
    assert(!decoder.finish());
    assert(pcm.size() < static_cast<std::size_t>(kRate));
}

}  // namespace

int main() {
    test_round_trip_length_and_header();
    test_chunking_does_not_change_output();
    test_explicit_48k_output_and_stereo();
    test_rejects_non_opus_and_bad_header();
    test_truncated_stream_fails_on_finish();
    return 0;
}
