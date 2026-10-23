// 从 stdin 读取交错 S16LE PCM，向 stdout 输出 Ogg Opus；统计信息以 JSON 写到 stderr。
// 文件 IO 只发生在本工具中，不进入 core 库。
//
// 用法：ogg_opus_encode [--rate 16000] [--channels 1] [--bitrate 24000] [--serial 1] < in.pcm > out.ogg
#include "jkmeeting/ogg_opus_encoder.h"

#include <algorithm>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <vector>

namespace {

bool parse_int(const char* text, long& value) {
    char* end = nullptr;
    value = std::strtol(text, &end, 10);
    return end != text && *end == '\0';
}

bool write_all(const std::vector<uint8_t>& bytes) {
    return bytes.empty() || std::fwrite(bytes.data(), 1, bytes.size(), stdout) == bytes.size();
}

}  // namespace

int main(int argc, char** argv) {
    jkmeeting::audio::OpusEncoderConfig config;
    long serial = 1;
    for (int i = 1; i + 1 < argc; i += 2) {
        long value = 0;
        if (!parse_int(argv[i + 1], value)) {
            std::fprintf(stderr, "invalid value for %s\n", argv[i]);
            return 2;
        }
        const std::string flag = argv[i];
        if (flag == "--rate") {
            config.sample_rate = static_cast<int>(value);
        } else if (flag == "--channels") {
            config.channels = static_cast<int>(value);
        } else if (flag == "--bitrate") {
            config.bitrate_bps = static_cast<int>(value);
        } else if (flag == "--serial") {
            serial = value;
        } else {
            std::fprintf(stderr, "unknown flag %s\n", argv[i]);
            return 2;
        }
    }

    jkmeeting::audio::OggOpusEncoder encoder(config, static_cast<uint32_t>(serial));
    if (!encoder.valid()) {
        std::fprintf(stderr, "invalid encoder config\n");
        return 2;
    }
    // 工具只处理短样本，整段读入后按 20 ms 分块送入编码器，模拟流式调用。
    std::vector<unsigned char> raw;
    unsigned char buffer[8'192];
    std::size_t read = 0;
    while ((read = std::fread(buffer, 1, sizeof(buffer), stdin)) > 0) {
        raw.insert(raw.end(), buffer, buffer + read);
    }
    const std::size_t frame_bytes = sizeof(int16_t) * static_cast<std::size_t>(config.channels);
    if (raw.size() % frame_bytes != 0) {
        std::fprintf(stderr, "input is not aligned to whole frames\n");
        return 1;
    }
    std::vector<int16_t> pcm(raw.size() / sizeof(int16_t));
    std::memcpy(pcm.data(), raw.data(), raw.size());

    std::vector<uint8_t> bytes = encoder.headers();
    const std::size_t chunk = static_cast<std::size_t>(config.sample_rate / 50 * config.channels);
    for (std::size_t offset = 0; offset < pcm.size(); offset += chunk) {
        const std::size_t count = std::min(chunk, pcm.size() - offset);
        if (!encoder.encode(pcm.data() + offset, count, bytes) || !write_all(bytes)) {
            std::fprintf(stderr, "encode failed\n");
            return 1;
        }
        bytes.clear();
    }
    if (!encoder.finish(bytes) || !write_all(bytes)) {
        std::fprintf(stderr, "finish failed (empty input?)\n");
        return 1;
    }
    std::fprintf(stderr, "{\"duration_ms\":%lld,\"packets\":%lld,\"bytes\":%lld}\n",
                 static_cast<long long>(encoder.duration_ms()), static_cast<long long>(encoder.packet_count()),
                 static_cast<long long>(encoder.bytes_written()));
    return 0;
}
