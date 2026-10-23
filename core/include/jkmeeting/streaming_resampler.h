#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace jkmeeting::audio {

// 有状态的流式重采样器：跨调用保留滤波历史与小数相位，
// 因此任意分块方式产生的输出逐样本一致。输入输出均为交错 S16。
class StreamingResampler {
public:
    StreamingResampler(int input_rate, int output_rate, int channels);

    [[nodiscard]] bool valid() const { return valid_; }

    // sample_count 必须是 channels 的整数倍；非法输入或 flush 之后返回空。
    [[nodiscard]] std::vector<int16_t> process(const int16_t* samples, std::size_t sample_count);

    // 补零冲刷尾部，使总输出帧数等于 round(total_input_frames * output / input)。
    [[nodiscard]] std::vector<int16_t> flush();

    [[nodiscard]] int64_t input_frames() const { return input_frames_; }
    [[nodiscard]] int64_t output_frames() const { return output_frames_; }

    // 单侧抽头数，以较低采样率的样本计；降采样时按 input/output 放大，保证过渡带足够窄。
    static constexpr int kHalfTaps = 16;
    static constexpr int kMaxPhases = 1024;

private:
    void append_input(const int16_t* samples, std::size_t frame_count);
    void append_zeros(std::size_t frame_count);
    void produce(std::vector<int16_t>& out, int64_t max_output_frames);

    bool valid_ = false;
    bool flushed_ = false;
    int channels_ = 0;
    int64_t up_ = 0;    // output_rate / gcd
    int64_t down_ = 0;  // input_rate / gcd
    int phase_count_ = 0;
    int half_taps_ = 0;  // 以输入样本计的单侧抽头数

    std::vector<float> coefficients_;  // phase_count_ * 2 * half_taps_
    std::vector<float> history_;       // 交错样本，history_[0] 对应 base_frame_
    int64_t base_frame_ = 0;           // history_ 首帧在零填充输入流中的位置

    int64_t next_position_ = 0;  // 下一个输出样本对应的输入整数位置（零填充流坐标）
    int64_t phase_ = 0;          // 小数部分分子，范围 [0, up_)

    int64_t input_frames_ = 0;
    int64_t output_frames_ = 0;
};

}  // namespace jkmeeting::audio
