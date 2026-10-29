#pragma once

#include <cstddef>
#include <cstdint>
#include <map>
#include <optional>
#include <vector>

namespace jkmeeting::audio {

// 实时推流用的抖动缓冲（纯计算）：按序号重排 20 ms 帧，丢包以静音填充。
// 播放端按固定节拍调用 pop()；数据未到且后续帧也未到时返回 nullopt（欠载，由调用方决定等待或静音）。
class JitterBuffer {
public:
    struct Config {
        std::size_t frame_samples = 320;  // 20 ms @ 16 kHz
        std::size_t max_buffered_frames = 50;  // 超过则丢弃最旧的待播帧，防止延迟无限增长
    };

    struct Output {
        std::vector<int16_t> samples;
        std::uint32_t sequence = 0;
        bool concealed = false;  // true 表示该帧为丢包静音填充
    };

    struct Stats {
        std::int64_t received = 0;
        std::int64_t emitted = 0;
        std::int64_t concealed = 0;
        std::int64_t duplicates_or_late = 0;
        std::int64_t overflow_dropped = 0;
        std::int64_t underruns = 0;
    };

    explicit JitterBuffer(const Config& config) : config_(config) {}

    // 入队一帧；序号小于下一个待播序号（已播出/已填充）或重复入队时丢弃并计数。
    void push(std::uint32_t sequence, std::vector<int16_t> samples);

    // 取下一帧待播数据：命中真实帧直接返回；缺失但已有更高序号帧时返回静音填充；尚无任何后续帧时返回 nullopt。
    std::optional<Output> pop();

    [[nodiscard]] Stats stats() const { return stats_; }
    [[nodiscard]] bool started() const { return started_; }

private:
    Config config_;
    std::map<std::uint32_t, std::vector<int16_t>> buffered_;
    std::uint32_t next_sequence_ = 0;
    bool started_ = false;
    Stats stats_;
};

}  // namespace jkmeeting::audio
