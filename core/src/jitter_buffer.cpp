#include "jkmeeting/jitter_buffer.h"

#include <algorithm>

namespace jkmeeting::audio {

void JitterBuffer::push(std::uint32_t sequence, std::vector<int16_t> samples) {
    if (!started_) {
        started_ = true;
        next_sequence_ = sequence;
    }
    if (sequence < next_sequence_ || buffered_.contains(sequence)) {
        ++stats_.duplicates_or_late;
        return;
    }
    // 帧长不符时截断/补零到固定帧长，保证播放节拍恒定。
    samples.resize(config_.frame_samples, 0);
    buffered_.emplace(sequence, std::move(samples));
    ++stats_.received;
    while (buffered_.size() > config_.max_buffered_frames) {
        // 丢弃最旧的待播帧并把播放序号推进到新的最旧帧，避免延迟无限累积。
        auto oldest = buffered_.begin();
        next_sequence_ = oldest->first;
        buffered_.erase(oldest);
        ++stats_.overflow_dropped;
    }
}

std::optional<JitterBuffer::Output> JitterBuffer::pop() {
    if (!started_) {
        ++stats_.underruns;
        return std::nullopt;
    }
    auto found = buffered_.find(next_sequence_);
    if (found != buffered_.end()) {
        Output output{std::move(found->second), next_sequence_, false};
        buffered_.erase(found);
        ++next_sequence_;
        ++stats_.emitted;
        return output;
    }
    if (!buffered_.empty()) {
        // 后续帧已到而当前帧缺失：判定丢包，静音填充并推进。
        Output output{std::vector<int16_t>(config_.frame_samples, 0), next_sequence_, true};
        ++next_sequence_;
        ++stats_.concealed;
        ++stats_.emitted;
        return output;
    }
    ++stats_.underruns;
    return std::nullopt;
}

}  // namespace jkmeeting::audio
