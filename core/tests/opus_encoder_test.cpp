#include "jkmeeting/opus_encoder.h"

#include "test_signals.h"

#include <opus.h>

#include <algorithm>
#include <cassert>
#include <cmath>
#include <cstdint>
#include <cstdio>
#include <vector>

namespace {

using jkmeeting::audio::EncodedPacket;
using jkmeeting::audio::OpusEncoderConfig;
using jkmeeting::audio::OpusPacketEncoder;

constexpr int kRate = 16'000;
constexpr int64_t kFrame = 320;

std::vector<int16_t> speech_band_signal(std::size_t frames) { return jkmeeting::test::speech_band(kRate, frames); }

std::vector<EncodedPacket> encode_all(OpusPacketEncoder& encoder, const std::vector<int16_t>& samples,
                                      std::size_t chunk) {
    std::vector<EncodedPacket> packets;
    for (std::size_t offset = 0; offset < samples.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, samples.size() - offset);
        assert(encoder.encode(samples.data() + offset, count, packets));
    }
    return packets;
}

std::vector<int16_t> decode(const std::vector<EncodedPacket>& packets, int channels) {
    int error = 0;
    OpusDecoder* decoder = opus_decoder_create(kRate, channels, &error);
    assert(error == OPUS_OK);
    std::vector<int16_t> pcm;
    std::vector<int16_t> buffer(static_cast<std::size_t>(5'760 * channels));
    for (const auto& packet : packets) {
        const int frames = opus_decode(decoder, packet.data.data(), static_cast<opus_int32>(packet.data.size()),
                                       buffer.data(), 5'760, 0);
        assert(frames == packet.frames);
        pcm.insert(pcm.end(), buffer.begin(), buffer.begin() + frames * channels);
    }
    opus_decoder_destroy(decoder);
    return pcm;
}

void test_one_second_mono_packets_and_flush() {
    OpusPacketEncoder encoder(OpusEncoderConfig{});
    assert(encoder.valid());
    const auto input = speech_band_signal(kRate);
    auto packets = encode_all(encoder, input, 160);
    assert(packets.size() == 50);
    assert(encoder.input_frames() == kRate);
    for (const auto& packet : packets) {
        assert(packet.frames == kFrame);
        assert(!packet.data.empty() && packet.data.size() <= 1'275);
    }

    const int lookahead = encoder.pre_skip_48k() / 3;
    assert(lookahead > 0 && lookahead < kFrame);
    assert(encoder.flush(packets));
    assert(packets.size() == 51);
    assert(encoder.encoded_frames() >= encoder.input_frames() + lookahead);
    assert(!encoder.flush(packets));
    assert(!encoder.encode(input.data(), 160, packets));
}

void test_decoded_signal_matches_input() {
    OpusPacketEncoder encoder(OpusEncoderConfig{});
    const auto input = speech_band_signal(static_cast<std::size_t>(kRate) * 2);
    auto packets = encode_all(encoder, input, 441);
    assert(encoder.flush(packets));

    const auto decoded = decode(packets, 1);
    const auto pre_skip = static_cast<std::size_t>(encoder.pre_skip_48k() / 3);
    assert(decoded.size() >= pre_skip + input.size());

    // 跳过前 100 ms 的编码器收敛期，比较其余部分。
    double signal = 0.0;
    double noise = 0.0;
    for (std::size_t i = kRate / 10; i < input.size(); ++i) {
        const double reference = input[i];
        const double diff = reference - decoded[pre_skip + i];
        signal += reference * reference;
        noise += diff * diff;
    }
    const double snr_db = 10.0 * std::log10(signal / noise);
    std::printf("{\"case\":\"opus_voip_24k_speech_band\",\"snr_db\":%.2f}\n", snr_db);
    // SILK 为参数化编码，不保真波形（实测 13.1 dB）；此处仅作回归护栏，语音质量以转写对比为准。
    assert(snr_db >= 12.0);
}

void test_stereo_counts_frames_per_channel() {
    OpusEncoderConfig config;
    config.channels = 2;
    OpusPacketEncoder encoder(config);
    assert(encoder.valid());
    std::vector<int16_t> interleaved(static_cast<std::size_t>(kRate) * 2);
    const auto mono = speech_band_signal(kRate);
    for (std::size_t i = 0; i < mono.size(); ++i) {
        interleaved[2 * i] = mono[i];
        interleaved[2 * i + 1] = static_cast<int16_t>(mono[i] / 2);
    }
    auto packets = encode_all(encoder, interleaved, 1'000);
    assert(packets.size() == 50);
    assert(encoder.input_frames() == kRate);
    assert(packets.front().frames == kFrame);

    std::vector<EncodedPacket> rejected;
    assert(!encoder.encode(interleaved.data(), 3, rejected));  // 不能被声道数整除
}

void test_invalid_configs_are_rejected() {
    const OpusEncoderConfig bad_rate{44'100, 1, 24'000, 20};
    const OpusEncoderConfig bad_channels{16'000, 3, 24'000, 20};
    const OpusEncoderConfig bad_frame{16'000, 1, 24'000, 15};
    const OpusEncoderConfig bad_bitrate{16'000, 1, 0, 20};
    for (const auto& config : {bad_rate, bad_channels, bad_frame, bad_bitrate}) {
        OpusPacketEncoder encoder(config);
        assert(!encoder.valid());
        std::vector<EncodedPacket> packets;
        const int16_t sample = 0;
        assert(!encoder.encode(&sample, 1, packets));
        assert(!encoder.flush(packets));
    }
}

void test_chunking_does_not_change_output() {
    const auto input = speech_band_signal(static_cast<std::size_t>(kRate) * 3 / 2);
    OpusPacketEncoder one_by_one(OpusEncoderConfig{});
    OpusPacketEncoder large(OpusEncoderConfig{});
    auto a = encode_all(one_by_one, input, 1);
    auto b = encode_all(large, input, 7'777);
    assert(one_by_one.flush(a));
    assert(large.flush(b));
    assert(a.size() == b.size());
    for (std::size_t i = 0; i < a.size(); ++i) {
        assert(a[i].data == b[i].data);
    }
}

void test_empty_input_flush_emits_nothing() {
    OpusPacketEncoder encoder(OpusEncoderConfig{});
    std::vector<EncodedPacket> packets;
    assert(encoder.flush(packets));
    assert(packets.empty());
}

}  // namespace

int main() {
    test_one_second_mono_packets_and_flush();
    test_decoded_signal_matches_input();
    test_stereo_counts_frames_per_channel();
    test_invalid_configs_are_rejected();
    test_chunking_does_not_change_output();
    test_empty_input_flush_emits_nothing();
    return 0;
}
