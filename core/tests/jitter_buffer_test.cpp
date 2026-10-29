#include "jkmeeting/jitter_buffer.h"

#include <cassert>
#include <cstdint>
#include <iostream>
#include <vector>

namespace {

using jkmeeting::audio::JitterBuffer;

std::vector<int16_t> Frame(std::uint32_t sequence, std::size_t samples = 320) {
    return std::vector<int16_t>(samples, static_cast<int16_t>(sequence % 30000));
}

void TestInOrder() {
    JitterBuffer buffer{JitterBuffer::Config{}};
    for (std::uint32_t seq = 0; seq < 10; ++seq) buffer.push(seq, Frame(seq));
    for (std::uint32_t seq = 0; seq < 10; ++seq) {
        auto out = buffer.pop();
        assert(out.has_value());
        assert(!out->concealed);
        assert(out->sequence == seq);
        assert(out->samples.size() == 320);
    }
    assert(buffer.stats().emitted == 10);
    assert(buffer.stats().concealed == 0);
    std::cout << "in_order ok\n";
}

void TestReorder() {
    JitterBuffer buffer{JitterBuffer::Config{}};
    buffer.push(0, Frame(0));
    buffer.push(2, Frame(2));
    buffer.push(1, Frame(1));
    buffer.push(3, Frame(3));
    for (std::uint32_t seq = 0; seq < 4; ++seq) {
        auto out = buffer.pop();
        assert(out.has_value() && out->sequence == seq && !out->concealed);
    }
    std::cout << "reorder ok\n";
}

void TestLossConcealmentAndLateDrop() {
    JitterBuffer buffer{JitterBuffer::Config{}};
    buffer.push(0, Frame(0));
    buffer.push(2, Frame(2));  // seq 1 丢失
    auto first = buffer.pop();
    assert(first.has_value() && first->sequence == 0);
    auto concealed = buffer.pop();
    assert(concealed.has_value() && concealed->concealed && concealed->sequence == 1);
    for (auto sample : concealed->samples) assert(sample == 0);
    // 迟到的 seq 1 应被丢弃
    buffer.push(1, Frame(1));
    assert(buffer.stats().duplicates_or_late == 1);
    auto third = buffer.pop();
    assert(third.has_value() && third->sequence == 2 && !third->concealed);
    std::cout << "loss_concealment ok\n";
}

// 30% 确定性伪随机丢包：输出总帧数与输入时间轴一致（误差 0），丢包段全部静音填充。
void TestThirtyPercentLossKeepsDuration() {
    JitterBuffer buffer{JitterBuffer::Config{}};
    constexpr std::uint32_t kFrames = 1500;  // 30 秒（20 ms/帧）
    std::uint32_t state = 42;
    auto next_random = [&state] {
        state = state * 1664525u + 1013904223u;
        return (state >> 16) % 100;
    };
    // 按播放节拍交错 push/pop（实时场景），而不是先灌满再取。
    std::int64_t dropped = 0;
    std::int64_t emitted = 0;
    std::int64_t concealed = 0;
    for (std::uint32_t seq = 0; seq < kFrames; ++seq) {
        if (seq != 0 && seq != kFrames - 1 && next_random() < 30) {
            ++dropped;
        } else {
            buffer.push(seq, Frame(seq));
        }
        if (auto out = buffer.pop()) {
            ++emitted;
            if (out->concealed) ++concealed;
            assert(out->samples.size() == 320);
        }
    }
    while (auto out = buffer.pop()) {
        ++emitted;
        if (out->concealed) ++concealed;
        assert(out->samples.size() == 320);
    }
    assert(emitted == kFrames);
    assert(concealed == dropped);
    std::cout << "loss_30pct ok dropped=" << dropped << " concealed=" << concealed << "\n";
}

void TestUnderrunBeforeStart() {
    JitterBuffer buffer{JitterBuffer::Config{}};
    assert(!buffer.pop().has_value());
    assert(buffer.stats().underruns == 1);
    std::cout << "underrun ok\n";
}

}  // namespace

int main() {
    TestInOrder();
    TestReorder();
    TestLossConcealmentAndLateDrop();
    TestThirtyPercentLossKeepsDuration();
    TestUnderrunBeforeStart();
    std::cout << "jitter_buffer_test passed\n";
    return 0;
}
