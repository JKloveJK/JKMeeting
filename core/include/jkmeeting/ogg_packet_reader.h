#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace jkmeeting::audio {

struct OggPacket {
    std::vector<uint8_t> data;
    int64_t page_granule = -1;  // 包结束所在页的 granule
    bool last_on_page = false;  // 只有页内最后一个完整包才对应该页 granule
    bool bos = false;
    bool eos = false;  // 包结束所在页带 EOS 标志
};

// RFC 3533 流式解析：字节可按任意方式分块喂入，输出与分块无关。
// 只支持单一逻辑流；出现第二个 serial、CRC 错误、魔数或版本错误、续包标志不一致时进入失败态。
class OggPacketReader {
public:
    // out 追加本次完整结束的包；失败后始终返回 false。
    bool push(const uint8_t* data, std::size_t size, std::vector<OggPacket>& out);

    // 已收到 EOS 页，且没有残留的半页或未结束的包。
    [[nodiscard]] bool complete() const;
    [[nodiscard]] bool failed() const { return failed_; }
    [[nodiscard]] int64_t pages() const { return pages_; }

private:
    bool parse_page(const uint8_t* page, std::size_t header_size, std::size_t body_size, std::vector<OggPacket>& out);

    std::vector<uint8_t> buffer_;
    std::vector<uint8_t> partial_packet_;
    bool has_partial_ = false;
    bool failed_ = false;
    bool eos_seen_ = false;
    bool serial_known_ = false;
    uint32_t serial_ = 0;
    uint32_t next_sequence_ = 0;
    int64_t pages_ = 0;
};

}  // namespace jkmeeting::audio
