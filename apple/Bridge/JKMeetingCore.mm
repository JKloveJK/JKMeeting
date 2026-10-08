#import "JKMeetingCore.h"

#include "jkmeeting/audio_processor.h"

#include <cstring>
#include <limits>
#include <new>
#include <vector>

NSErrorDomain const JKMeetingCoreErrorDomain = @"com.jkmeeting.core";

namespace {

void assign_error(NSError* _Nullable * _Nullable error, JKMeetingCoreErrorCode code, NSString* message) {
    if (error != nullptr) {
        *error = [NSError errorWithDomain:JKMeetingCoreErrorDomain
                                     code:code
                                 userInfo:@{NSLocalizedDescriptionKey : message}];
    }
}

}  // namespace

@interface JKMeetingAudioFrame ()

- (instancetype)initWithPCMData:(NSData*)pcmData
                     sampleRate:(NSInteger)sampleRate
                       channels:(NSInteger)channels
                    timestampMs:(int64_t)timestampMs;

@end

@implementation JKMeetingAudioFrame

- (instancetype)initWithPCMData:(NSData*)pcmData
                     sampleRate:(NSInteger)sampleRate
                       channels:(NSInteger)channels
                    timestampMs:(int64_t)timestampMs {
    self = [super init];
    if (self != nil) {
        _pcmData = [pcmData copy];
        _sampleRate = sampleRate;
        _channels = channels;
        _timestampMs = timestampMs;
    }
    return self;
}

@end

@implementation JKMeetingCore

- (nullable JKMeetingAudioFrame*)resamplePCMData:(NSData*)pcmData
                                      sampleRate:(NSInteger)sampleRate
                                        channels:(NSInteger)channels
                                targetSampleRate:(NSInteger)targetSampleRate
                                     timestampMs:(int64_t)timestampMs
                                           error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }

    if (pcmData.length == 0 || pcmData.length % sizeof(int16_t) != 0 || sampleRate <= 0 ||
        channels <= 0 || targetSampleRate <= 0 ||
        pcmData.length / sizeof(int16_t) % static_cast<NSUInteger>(channels) != 0 ||
        sampleRate > std::numeric_limits<int>::max() || channels > std::numeric_limits<int>::max() ||
        targetSampleRate > std::numeric_limits<int>::max()) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid S16LE PCM frame metadata.");
        return nil;
    }

    try {
        const std::size_t sampleCount = pcmData.length / sizeof(int16_t);
        std::vector<int16_t> copiedSamples(sampleCount);
        std::memcpy(copiedSamples.data(), pcmData.bytes, pcmData.length);

        const jkmeeting::audio::AudioFrameView input{
            .samples = copiedSamples.data(),
            .sample_count = copiedSamples.size(),
            .sample_rate = static_cast<int>(sampleRate),
            .channels = static_cast<int>(channels),
            .timestamp_ms = timestampMs,
        };
        const jkmeeting::audio::OwnedAudioFrame output =
            jkmeeting::audio::AudioProcessor{}.resample(input, static_cast<int>(targetSampleRate));
        if (output.samples.empty()) {
            assign_error(error, JKMeetingCoreErrorProcessingFailed, @"Core produced an empty PCM frame.");
            return nil;
        }

        NSData* outputData = [NSData dataWithBytes:output.samples.data()
                                                   length:output.samples.size() * sizeof(int16_t)];
        return [[JKMeetingAudioFrame alloc] initWithPCMData:outputData
                                                  sampleRate:output.sample_rate
                                                    channels:output.channels
                                                 timestampMs:output.timestamp_ms];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for PCM processing.");
        return nil;
    }
}

@end
