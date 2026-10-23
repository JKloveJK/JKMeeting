#pragma once

#include "jkmeeting/ogg_opus_muxer.h"
#include "jkmeeting/opus_encoder.h"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace jkmeeting::audio {

// PCM -> Ogg Opus 字节流。调用顺序：headers() 一次 -> encode() 若干次 -> finish() 一次。
// 输出确定：相同输入与 serial 产生逐字节相同的结果，与分块方式无关。
class OggOpusEncoder {
public:
    OggOpusEncoder(const OpusEncoderConfig& config, uint32_t serial);

    [[nodiscard]] bool valid() const;

    std::vector<uint8_t> headers();

    // out 追加本次封好的页，可能不追加任何字节。
    bool encode(const int16_t* samples, std::size_t sample_count, std::vector<uint8_t>& out);

    // 冲刷编码器并写出 EOS 页；没有任何输入样本时返回 false。
    bool finish(std::vector<uint8_t>& out);

    // 由输入样本数推导，不依赖解码器。
    [[nodiscard]] int64_t duration_ms() const;
    [[nodiscard]] int64_t packet_count() const { return packet_count_; }
    [[nodiscard]] int64_t bytes_written() const { return bytes_written_; }

private:
    bool mux(std::vector<EncodedPacket>& packets, std::vector<uint8_t>& out);

    OpusPacketEncoder encoder_;
    OggOpusMuxer muxer_;
    bool headers_written_ = false;
    bool failed_ = false;
    int64_t granule_ = 0;
    int64_t packet_count_ = 0;
    int64_t bytes_written_ = 0;
    std::vector<EncodedPacket> packets_;
};

}  // namespace jkmeeting::audio
