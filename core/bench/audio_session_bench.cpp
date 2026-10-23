// 30 秒 48 kHz 单声道、每帧 1,024 样本，重采样到 16 kHz，再编码为 Ogg Opus，并解码回 PCM；每条管线输出一行 JSON。
#include "jkmeeting/audio_processor.h"
#include "jkmeeting/audio_session.h"
#include "jkmeeting/ogg_opus_decoder.h"
#include "jkmeeting/ogg_opus_encoder.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <numbers>
#include <vector>

namespace {

using namespace jkmeeting::audio;
using Clock = std::chrono::steady_clock;

constexpr int kInputRate = 48'000;
constexpr int kOutputRate = 16'000;
constexpr int kSeconds = 30;
constexpr std::size_t kFrame = 1'024;
constexpr int kIterations = 20;

std::vector<int16_t> make_input(int rate) {
    std::vector<int16_t> samples(static_cast<std::size_t>(rate) * kSeconds);
    for (std::size_t i = 0; i < samples.size(); ++i) {
        const double t = static_cast<double>(i) / rate;
        const bool voiced = static_cast<int>(t) % 3 != 2;  // 每 3 秒中 1 秒静音
        const double value = voiced ? 6'000.0 * std::sin(2.0 * std::numbers::pi * 220.0 * t) +
                                          2'000.0 * std::sin(2.0 * std::numbers::pi * 1'800.0 * t)
                                    : 0.0;
        samples[i] = static_cast<int16_t>(std::lround(value));
    }
    return samples;
}

double run_session(const std::vector<int16_t>& input, bool encode) {
    const auto start = Clock::now();
    AudioSession session({.input_rate = kInputRate, .output_rate = kOutputRate, .channels = 1});
    OggOpusEncoder encoder(OpusEncoderConfig{}, 1);
    std::vector<uint8_t> bytes = encoder.headers();
    OwnedAudioFrame out;
    for (std::size_t offset = 0; offset < input.size(); offset += kFrame) {
        const std::size_t count = std::min(kFrame, input.size() - offset);
        session.process({.samples = input.data() + offset,
                         .sample_count = count,
                         .sample_rate = kInputRate,
                         .channels = 1,
                         .sample_time = static_cast<int64_t>(offset),
                         .host_time_ns = static_cast<int64_t>(offset) * 1'000'000'000 / kInputRate + 1},
                        out);
        if (encode && !out.samples.empty()) {
            encoder.encode(out.samples.data(), out.samples.size(), bytes);
        }
    }
    session.finish(out);
    if (encode) {
        encoder.encode(out.samples.data(), out.samples.size(), bytes);
        encoder.finish(bytes);
    }
    return std::chrono::duration<double, std::milli>(Clock::now() - start).count();
}

double run_ogg_opus(const std::vector<int16_t>& input) {
    const auto start = Clock::now();
    OggOpusEncoder encoder(OpusEncoderConfig{}, 1);
    std::vector<uint8_t> bytes = encoder.headers();
    constexpr std::size_t kEncodeChunk = 320;
    for (std::size_t offset = 0; offset < input.size(); offset += kEncodeChunk) {
        encoder.encode(input.data() + offset, std::min(kEncodeChunk, input.size() - offset), bytes);
    }
    encoder.finish(bytes);
    return std::chrono::duration<double, std::milli>(Clock::now() - start).count();
}

std::vector<uint8_t> encode_ogg(const std::vector<int16_t>& input) {
    OggOpusEncoder encoder(OpusEncoderConfig{}, 1);
    std::vector<uint8_t> bytes = encoder.headers();
    encoder.encode(input.data(), input.size(), bytes);
    encoder.finish(bytes);
    return bytes;
}

// 按 4 KiB 分块喂入，模拟壳层边读文件边解码。
double run_ogg_opus_decode(const std::vector<uint8_t>& bytes) {
    const auto start = Clock::now();
    OggOpusDecoder decoder;
    std::vector<int16_t> pcm;
    constexpr std::size_t kReadChunk = 4'096;
    for (std::size_t offset = 0; offset < bytes.size(); offset += kReadChunk) {
        decoder.push(bytes.data() + offset, std::min(kReadChunk, bytes.size() - offset), pcm);
    }
    if (!decoder.finish() || pcm.size() != static_cast<std::size_t>(kOutputRate) * kSeconds) {
        std::fprintf(stderr, "decode mismatch\n");
    }
    return std::chrono::duration<double, std::milli>(Clock::now() - start).count();
}

double run_nearest_neighbor(const std::vector<int16_t>& input) {
    const auto start = Clock::now();
    const AudioProcessor processor;
    for (std::size_t offset = 0; offset < input.size(); offset += kFrame) {
        const std::size_t count = std::min(kFrame, input.size() - offset);
        const auto out = processor.resample({.samples = input.data() + offset,
                                             .sample_count = count,
                                             .sample_rate = kInputRate,
                                             .channels = 1,
                                             .timestamp_ms = 0},
                                            kOutputRate);
        (void)out;
    }
    return std::chrono::duration<double, std::milli>(Clock::now() - start).count();
}

double percentile(std::vector<double> values, double p) {
    std::sort(values.begin(), values.end());
    const auto index = static_cast<std::size_t>(std::ceil(p * static_cast<double>(values.size()))) - 1;
    return values[std::min(index, values.size() - 1)];
}

void report(const char* name, std::size_t frame, const std::vector<double>& durations) {
    const double audio_ms = kSeconds * 1'000.0;
    const double p50 = percentile(durations, 0.50);
    const double p95 = percentile(durations, 0.95);
    const double p99 = percentile(durations, 0.99);
    std::printf(
        "{\"pipeline\":\"%s\",\"audio_ms\":%.0f,\"frame\":%zu,\"iterations\":%d,"
        "\"p50_ms\":%.3f,\"p95_ms\":%.3f,\"p99_ms\":%.3f,"
        "\"p50_ratio_pct\":%.4f,\"p95_ratio_pct\":%.4f,\"p99_ratio_pct\":%.4f}\n",
        name, audio_ms, frame, kIterations, p50, p95, p99, p50 / audio_ms * 100, p95 / audio_ms * 100,
        p99 / audio_ms * 100);
}

}  // namespace

int main() {
    const auto input = make_input(kInputRate);
    const auto input_16k = make_input(kOutputRate);
    std::vector<double> session_ms;
    std::vector<double> nearest_ms;
    std::vector<double> opus_ms;
    std::vector<double> session_opus_ms;
    std::vector<double> decode_ms;
    const auto encoded = encode_ogg(input_16k);
    for (int i = 0; i < kIterations; ++i) {
        session_ms.push_back(run_session(input, false));
        nearest_ms.push_back(run_nearest_neighbor(input));
        opus_ms.push_back(run_ogg_opus(input_16k));
        session_opus_ms.push_back(run_session(input, true));
        decode_ms.push_back(run_ogg_opus_decode(encoded));
    }
    report("nearest_neighbor_resample", kFrame, nearest_ms);
    report("audio_session", kFrame, session_ms);
    report("ogg_opus_encode", 320, opus_ms);  // ratio 即 RTF x 100
    report("audio_session_then_ogg_opus", kFrame, session_opus_ms);
    report("ogg_opus_decode", 4'096, decode_ms);  // frame 为读取块字节数；ratio 即 RTF x 100
    return 0;
}
