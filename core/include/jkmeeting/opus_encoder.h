#pragma once

#include <cstddef>
#include <cstdint>
#include <memory>
#include <vector>

namespace jkmeeting::audio {

struct OpusEncoderConfig {
    int sample_rate = 16'000;  // 仅 8000/12000/16000/24000/48000
    int channels = 1;          // 1 或 2
    int bitrate_bps = 24'000;  // VBR 目标码率
    int frame_ms = 20;         // 仅 10/20/40/60
};

struct EncodedPacket {
    std::vector<uint8_t> data;
    int64_t frames = 0;  // 每声道帧数，以输入采样率计
};

// libopus 的薄封装：VOIP 模式、语音信号提示、VBR。非线程安全。
// sample_count 是交错后的采样值总数，须能被 channels 整除。
class OpusPacketEncoder {
public:
    explicit OpusPacketEncoder(const OpusEncoderConfig& config);
    ~OpusPacketEncoder();
    OpusPacketEncoder(const OpusPacketEncoder&) = delete;
    OpusPacketEncoder& operator=(const OpusPacketEncoder&) = delete;

    [[nodiscard]] bool valid() const;

    // 不足一帧的样本留在内部缓冲；失败后编码器不可恢复。
    bool encode(const int16_t* samples, std::size_t sample_count, std::vector<EncodedPacket>& out);

    // 补零编码，直到已编码帧数 >= 输入帧数 + lookahead，保证所有真实样本都可被解码。
    bool flush(std::vector<EncodedPacket>& out);

    [[nodiscard]] int pre_skip_48k() const;
    [[nodiscard]] int64_t input_frames() const;
    [[nodiscard]] int64_t encoded_frames() const;
    [[nodiscard]] const OpusEncoderConfig& config() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

}  // namespace jkmeeting::audio
