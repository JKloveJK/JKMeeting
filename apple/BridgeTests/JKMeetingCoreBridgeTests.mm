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

void test_session_fills_gap_and_reports_stats() {
    NSError* error = nil;
    JKMeetingAudioSession* session = [[JKMeetingAudioSession alloc] initWithInputSampleRate:48'000
                                                                                   channels:1
                                                                           outputSampleRate:16'000
                                                                                      error:&error];
    assert(session != nil);
    assert(error == nil);

    const std::vector<int16_t> frame(480, 4'000);
    std::size_t outputSamples = 0;
    for (const int64_t sampleTime : {0, 480, 1'440, 1'920}) {
        JKMeetingAudioFrame* output = [session processPCMData:data_from_samples(frame)
                                                   sampleTime:sampleTime
                                                   hostTimeNs:JKMeetingInvalidHostTime
                                                        error:&error];
        assert(output != nil);
        assert(error == nil);
        outputSamples += output.pcmData.length / sizeof(int16_t);
    }
    JKMeetingAudioFrame* tail = [session finishWithError:&error];
    assert(tail != nil);
    outputSamples += tail.pcmData.length / sizeof(int16_t);

    assert(outputSamples == 2'400 / 3);
    assert(session.stats.gapEvents == 1);
    assert(session.stats.filledGapFrames == 480);
    assert(session.stats.outputFrames == 800);

    assert([session processPCMData:data_from_samples(frame) sampleTime:2'400 hostTimeNs:0 error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorInvalidInput);
    error = nil;
    assert([session finishWithError:&error] == nil);
    assert(error != nil);
}

void test_session_rejects_invalid_configuration_and_pcm() {
    NSError* error = nil;
    assert([[JKMeetingAudioSession alloc] initWithInputSampleRate:0 channels:1 outputSampleRate:16'000
                                                            error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorInvalidInput);

    error = nil;
    JKMeetingAudioSession* session = [[JKMeetingAudioSession alloc] initWithInputSampleRate:48'000
                                                                                   channels:1
                                                                           outputSampleRate:16'000
                                                                                      error:&error];
    const uint8_t bytes[] = {0x00, 0x01, 0x02};
    assert([session processPCMData:[NSData dataWithBytes:bytes length:sizeof(bytes)]
                        sampleTime:JKMeetingInvalidSampleTime
                        hostTimeNs:JKMeetingInvalidHostTime
                             error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorInvalidInput);
    assert(session.stats.rejectedFrames == 0);
}

void test_ogg_opus_encoder_produces_stream() {
    NSError* error = nil;
    JKMeetingOggOpusEncoder* encoder = [[JKMeetingOggOpusEncoder alloc] initWithSampleRate:16'000
                                                                                  channels:1
                                                                                bitrateBps:24'000
                                                                                    serial:99
                                                                                     error:&error];
    assert(encoder != nil);
    assert(error == nil);

    NSMutableData* stream = [[encoder headerData] mutableCopy];
    assert(stream.length > 0);
    assert(std::memcmp(stream.bytes, "OggS", 4) == 0);
    assert([encoder headerData].length == 0);

    std::vector<int16_t> frame(160);
    for (std::size_t i = 0; i < frame.size(); ++i) {
        frame[i] = static_cast<int16_t>((i % 32) * 500);
    }
    for (int i = 0; i < 100; ++i) {  // 1 秒
        NSData* bytes = [encoder encodePCMData:data_from_samples(frame) error:&error];
        assert(bytes != nil);
        assert(error == nil);
        [stream appendData:bytes];
    }
    NSData* tail = [encoder finishWithError:&error];
    assert(tail != nil && tail.length > 0);
    [stream appendData:tail];
    assert(encoder.durationMs == 1'000);
    assert(encoder.packetCount == 51);
    assert(encoder.bytesWritten == static_cast<int64_t>(stream.length));

    assert([encoder encodePCMData:data_from_samples(frame) error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorProcessingFailed);
    error = nil;
    assert([encoder finishWithError:&error] == nil);
    assert(error != nil);
}

void test_ogg_opus_encoder_rejects_invalid_input() {
    NSError* error = nil;
    assert([[JKMeetingOggOpusEncoder alloc] initWithSampleRate:44'100 channels:1 bitrateBps:24'000 serial:1
                                                         error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorInvalidInput);

    error = nil;
    JKMeetingOggOpusEncoder* encoder = [[JKMeetingOggOpusEncoder alloc] initWithSampleRate:16'000
                                                                                  channels:2
                                                                                bitrateBps:24'000
                                                                                    serial:1
                                                                                     error:&error];
    assert(encoder != nil);
    (void)[encoder headerData];
    const int16_t odd[] = {1, 2, 3};  // 不能被声道数整除
    assert([encoder encodePCMData:[NSData dataWithBytes:odd length:sizeof(odd)] error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorInvalidInput);

    error = nil;
    JKMeetingOggOpusEncoder* empty = [[JKMeetingOggOpusEncoder alloc] initWithSampleRate:16'000
                                                                                channels:1
                                                                              bitrateBps:24'000
                                                                                  serial:1
                                                                                   error:&error];
    (void)[empty headerData];
    assert([empty finishWithError:&error] == nil);  // 零长度录音
    assert(error.code == JKMeetingCoreErrorProcessingFailed);
}

void test_ogg_opus_decoder_round_trips_encoder_output() {
    NSError* error = nil;
    JKMeetingOggOpusEncoder* encoder = [[JKMeetingOggOpusEncoder alloc] initWithSampleRate:16'000
                                                                                  channels:1
                                                                                bitrateBps:24'000
                                                                                    serial:7
                                                                                     error:&error];
    NSMutableData* stream = [[encoder headerData] mutableCopy];
    std::vector<int16_t> pcm(16'000 + 37);
    for (std::size_t i = 0; i < pcm.size(); ++i) {
        pcm[i] = static_cast<int16_t>((i % 40) * 300);
    }
    [stream appendData:[encoder encodePCMData:data_from_samples(pcm) error:&error]];
    [stream appendData:[encoder finishWithError:&error]];

    JKMeetingOggOpusDecoder* decoder = [[JKMeetingOggOpusDecoder alloc] init];
    assert(decoder != nil);
    assert(decoder.sampleRate == 0);
    NSMutableData* decoded = [NSMutableData data];
    const NSUInteger chunk = 333;
    for (NSUInteger offset = 0; offset < stream.length; offset += chunk) {
        NSData* part = [stream subdataWithRange:NSMakeRange(offset, MIN(chunk, stream.length - offset))];
        NSData* out = [decoder decodeData:part error:&error];
        assert(out != nil);
        [decoded appendData:out];
    }
    assert([decoder finishWithError:&error]);
    assert(decoder.sampleRate == 16'000);
    assert(decoder.channels == 1);
    assert(decoder.outputFrames == static_cast<int64_t>(pcm.size()));
    assert(decoded.length == pcm.size() * sizeof(int16_t));
}

void test_ogg_opus_decoder_rejects_garbage_and_truncation() {
    NSError* error = nil;
    JKMeetingOggOpusDecoder* garbage = [[JKMeetingOggOpusDecoder alloc] init];
    const char junk[] = "this is not an ogg stream at all";
    assert([garbage decodeData:[NSData dataWithBytes:junk length:sizeof(junk)] error:&error] == nil);
    assert(error.code == JKMeetingCoreErrorProcessingFailed);

    error = nil;
    JKMeetingOggOpusDecoder* empty = [[JKMeetingOggOpusDecoder alloc] init];
    assert(![empty finishWithError:&error]);
    assert(error.code == JKMeetingCoreErrorProcessingFailed);
}

}  // namespace

int main() {
    @autoreleasepool {
        test_bridge_resamples_pcm_and_preserves_metadata();
        test_bridge_rejects_misaligned_pcm_data();
        test_session_fills_gap_and_reports_stats();
        test_session_rejects_invalid_configuration_and_pcm();
        test_ogg_opus_encoder_produces_stream();
        test_ogg_opus_encoder_rejects_invalid_input();
        test_ogg_opus_decoder_round_trips_encoder_output();
        test_ogg_opus_decoder_rejects_garbage_and_truncation();
    }
    return 0;
}
