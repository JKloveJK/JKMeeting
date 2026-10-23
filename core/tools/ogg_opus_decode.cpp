// 从 stdin 读取 Ogg Opus，向 stdout 输出交错 S16LE PCM；统计信息以 JSON 写到 stderr。
// 文件 IO 只发生在本工具中，不进入 core 库。
//
// 用法：ogg_opus_decode [--rate 0] < in.ogg > out.pcm（--rate 0 表示跟随 OpusHead 原始采样率）
#include "jkmeeting/ogg_opus_decoder.h"

#include <cstdio>
#include <cstdlib>
#include <string>
#include <vector>

int main(int argc, char** argv) {
    int rate = 0;
    for (int i = 1; i + 1 < argc; i += 2) {
        char* end = nullptr;
        const long value = std::strtol(argv[i + 1], &end, 10);
        if (std::string(argv[i]) != "--rate" || end == argv[i + 1] || *end != '\0') {
            std::fprintf(stderr, "usage: ogg_opus_decode [--rate 0|8000|12000|16000|24000|48000]\n");
            return 2;
        }
        rate = static_cast<int>(value);
    }

    jkmeeting::audio::OggOpusDecoder decoder(rate);
    std::vector<int16_t> pcm;
    unsigned char buffer[4'096];
    std::size_t read = 0;
    while ((read = std::fread(buffer, 1, sizeof(buffer), stdin)) > 0) {
        pcm.clear();
        if (!decoder.push(buffer, read, pcm)) {
            std::fprintf(stderr, "decode failed\n");
            return 1;
        }
        if (!pcm.empty() && std::fwrite(pcm.data(), sizeof(int16_t), pcm.size(), stdout) != pcm.size()) {
            std::fprintf(stderr, "write failed\n");
            return 1;
        }
    }
    if (!decoder.finish()) {
        std::fprintf(stderr, "stream incomplete\n");
        return 1;
    }
    std::fprintf(stderr, "{\"sample_rate\":%d,\"channels\":%d,\"frames\":%lld,\"packets\":%lld,\"pre_skip_48k\":%d}\n",
                 decoder.sample_rate(), decoder.channels(), static_cast<long long>(decoder.output_frames()),
                 static_cast<long long>(decoder.packet_count()), decoder.pre_skip_48k());
    return 0;
}
