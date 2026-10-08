#import "JKMeetingCore.h"

#import <Foundation/Foundation.h>

#include <cassert>
#include <cstdint>
#include <cstring>
#include <vector>

namespace {

NSData* data_from_samples(const std::vector<int16_t>& samples) {
    return [NSData dataWithBytes:samples.data() length:samples.size() * sizeof(int16_t)];
}

std::vector<int16_t> samples_from_data(NSData* data) {
    assert(data.length % sizeof(int16_t) == 0);
    std::vector<int16_t> samples(data.length / sizeof(int16_t));
    std::memcpy(samples.data(), data.bytes, data.length);
    return samples;
}

void test_bridge_resamples_pcm_and_preserves_metadata() {
    JKMeetingCore* core = [[JKMeetingCore alloc] init];
    NSError* error = nil;
    JKMeetingAudioFrame* output = [core resamplePCMData:data_from_samples({0, 1000, 2000, 3000})
                                             sampleRate:4
                                               channels:1
                                       targetSampleRate:2
                                            timestampMs:42
                                                  error:&error];

    assert(error == nil);
    assert(output != nil);
    assert(output.sampleRate == 2);
    assert(output.channels == 1);
    assert(output.timestampMs == 42);
    assert((samples_from_data(output.pcmData) == std::vector<int16_t>{0, 2000}));
}

void test_bridge_rejects_misaligned_pcm_data() {
    JKMeetingCore* core = [[JKMeetingCore alloc] init];
    const uint8_t bytes[] = {0x00, 0x01, 0x02};
    NSError* error = nil;
    JKMeetingAudioFrame* output = [core resamplePCMData:[NSData dataWithBytes:bytes length:sizeof(bytes)]
                                             sampleRate:16'000
                                               channels:1
                                       targetSampleRate:8'000
                                            timestampMs:0
                                                  error:&error];

    assert(output == nil);
    assert(error != nil);
}

}  // namespace

int main() {
    @autoreleasepool {
        test_bridge_resamples_pcm_and_preserves_metadata();
        test_bridge_rejects_misaligned_pcm_data();
    }
    return 0;
}
