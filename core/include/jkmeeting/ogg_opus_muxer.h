#pragma once

#include "jkmeeting/opus_encoder.h"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace jkmeeting::audio {

// Ogg CRC32：多项式 0x04C11DB7，初值 0，不反射，不异或（RFC 3533）。
uint32_t ogg_crc(const uint8_t* data, std::size_t size);

// 按 RFC 3533 / RFC 7845 生成 Ogg Opus 字节流，只做字节生成，不做 IO。
// granule 均以 48 kHz 每声道样本计：音频页为累计解码样本数（含 pre-skip 部分），
// 末页为 pre_skip + 输入帧数换算值，用于裁掉末帧补零。
class OggOpusMuxer {
public:
    OggOpusMuxer(uint32_t serial, int channels, int input_sample_rate, int pre_skip_48k);

    // 声道仅支持 1~2（映射族 0），pre_skip 须能放进 uint16。
    [[nodiscard]] bool valid() const { return valid_; }

    // OpusHead 页（BOS）+ OpusTags 页；仅首次调用返回内容。
    std::vector<uint8_t> headers();

    // 包先进入未封口的当前页，只有当前页放不下时才输出该页；out 追加已封好的页。
    bool append(const EncodedPacket& packet, int64_t granule_end_48k, std::vector<uint8_t>& out);

    // 输出当前页并标记 EOS。没有任何音频包或 granule 越界时返回 false。
    bool finish(int64_t final_granule_48k, std::vector<uint8_t>& out);

private:
    void write_page(uint8_t header_type, int64_t granule, const std::vector<uint8_t>& lacing,
                    const std::vector<uint8_t>& body, std::vector<uint8_t>& out);
    void seal_current_page(uint8_t header_type, int64_t granule, std::vector<uint8_t>& out);

    uint32_t serial_;
    int channels_;
    int input_sample_rate_;
    int pre_skip_48k_;
    bool valid_;
    uint32_t sequence_ = 0;
    bool headers_written_ = false;
    bool finished_ = false;
    std::vector<uint8_t> lacing_;
    std::vector<uint8_t> body_;
    std::size_t page_packets_ = 0;
    int64_t page_granule_ = 0;
    int64_t last_sealed_granule_ = 0;
    int64_t audio_packets_ = 0;
};

}  // namespace jkmeeting::audio
