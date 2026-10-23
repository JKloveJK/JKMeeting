#pragma once

#include "jkmeeting/ogg_packet_reader.h"

#include <cstddef>
#include <cstdint>
#include <memory>
#include <vector>

namespace jkmeeting::audio {

// Ogg Opus 字节流 -> 交错 int16 PCM，只处理内存字节，不做 IO。
// 按 OpusHead 的 pre_skip 裁头、按 EOS 页 granule 裁尾（RFC 7845 §4.2 / §4.4），
// 因此本项目编码器的输出解码后与编码输入逐样本等长。仅支持映射族 0（1～2 声道）。
class OggOpusDecoder {
public:
    // output_sample_rate 为 0 时跟随 OpusHead 的原始采样率；该值不是 Opus 支持的采样率时使用 48 kHz。
    explicit OggOpusDecoder(int output_sample_rate = 0);
    ~OggOpusDecoder();
    OggOpusDecoder(const OggOpusDecoder&) = delete;
    OggOpusDecoder& operator=(const OggOpusDecoder&) = delete;

    // out 追加本次可输出的样本；输入非法或解码失败后始终返回 false。
    bool push(const uint8_t* data, std::size_t size, std::vector<int16_t>& out);

    // 流须完整结束（收到 EOS 且无残留字节）；否则返回 false。
    bool finish();

    [[nodiscard]] bool header_parsed() const;
    [[nodiscard]] int sample_rate() const;
    [[nodiscard]] int channels() const;
    [[nodiscard]] int pre_skip_48k() const;
    [[nodiscard]] int64_t output_frames() const;
    [[nodiscard]] int64_t packet_count() const;

private:
    struct Impl;
    std::unique_ptr<Impl> impl_;
};

}  // namespace jkmeeting::audio
