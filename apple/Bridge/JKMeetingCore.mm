#import "JKMeetingCore.h"

#include "jkmeeting/audio_processor.h"
#include "jkmeeting/audio_session.h"
#include "jkmeeting/ogg_opus_decoder.h"
#include "jkmeeting/ogg_opus_encoder.h"

#include <cstdint>
#include <cstring>
#include <limits>
#include <memory>
#include <new>
#include <vector>

NSErrorDomain const JKMeetingCoreErrorDomain = @"com.jkmeeting.core";
const int64_t JKMeetingInvalidSampleTime = jkmeeting::audio::FrameTimeline::kInvalidSampleTime;
const int64_t JKMeetingInvalidHostTime = jkmeeting::audio::FrameTimeline::kInvalidHostTime;

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

@interface JKMeetingAudioSessionStats ()

- (instancetype)initWithStats:(const jkmeeting::audio::SessionStats&)stats;

@end

@implementation JKMeetingAudioSessionStats

- (instancetype)initWithStats:(const jkmeeting::audio::SessionStats&)stats {
    self = [super init];
    if (self != nil) {
        _inputFrames = stats.input_frames;
        _outputFrames = stats.output_frames;
        _rejectedFrames = stats.rejected_frames;
        _gapEvents = stats.gap_events;
        _filledGapFrames = stats.filled_gap_frames;
        _unfilledGapFrames = stats.unfilled_gap_frames;
        _overlapFramesDiscarded = stats.overlap_frames_discarded;
        _jitterMs = stats.jitter_ms;
        _maxTransitDeltaMs = stats.max_transit_delta_ms;
        _speechMs = stats.speech_ms;
        _silenceMs = stats.silence_ms;
        _speechSegments = stats.speech_segments;
    }
    return self;
}

@end

@implementation JKMeetingAudioSession {
    std::unique_ptr<jkmeeting::audio::AudioSession> _session;
    int _inputSampleRate;
    int _channels;
    int _outputSampleRate;
}

- (nullable instancetype)initWithInputSampleRate:(NSInteger)inputSampleRate
                                        channels:(NSInteger)channels
                                outputSampleRate:(NSInteger)outputSampleRate
                                           error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    const NSInteger intMax = std::numeric_limits<int>::max();
    if (inputSampleRate <= 0 || channels <= 0 || outputSampleRate <= 0 || inputSampleRate > intMax ||
        channels > intMax || outputSampleRate > intMax) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid audio session configuration.");
        return nil;
    }

    self = [super init];
    if (self == nil) {
        return nil;
    }
    _inputSampleRate = static_cast<int>(inputSampleRate);
    _channels = static_cast<int>(channels);
    _outputSampleRate = static_cast<int>(outputSampleRate);
    try {
        _session = std::make_unique<jkmeeting::audio::AudioSession>(jkmeeting::audio::SessionConfig{
            .input_rate = _inputSampleRate,
            .output_rate = _outputSampleRate,
            .channels = _channels,
        });
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for audio session.");
        return nil;
    }
    if (!_session->valid()) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid audio session configuration.");
        return nil;
    }
    return self;
}

- (JKMeetingAudioFrame*)frameFrom:(const jkmeeting::audio::OwnedAudioFrame&)output {
    NSData* data = [NSData dataWithBytes:output.samples.data() length:output.samples.size() * sizeof(int16_t)];
    return [[JKMeetingAudioFrame alloc] initWithPCMData:data
                                             sampleRate:_outputSampleRate
                                               channels:_channels
                                            timestampMs:output.timestamp_ms];
}

- (nullable JKMeetingAudioFrame*)processPCMData:(NSData*)pcmData
                                     sampleTime:(int64_t)sampleTime
                                     hostTimeNs:(int64_t)hostTimeNs
                                          error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    const std::size_t bytesPerFrame = sizeof(int16_t) * static_cast<std::size_t>(_channels);
    if (pcmData.length == 0 || pcmData.length % bytesPerFrame != 0) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid S16LE PCM frame length.");
        return nil;
    }

    try {
        const std::size_t sampleCount = pcmData.length / sizeof(int16_t);
        const auto* bytes = static_cast<const uint8_t*>(pcmData.bytes);
        std::vector<int16_t> aligned;
        const int16_t* samples = reinterpret_cast<const int16_t*>(bytes);
        if (reinterpret_cast<std::uintptr_t>(bytes) % alignof(int16_t) != 0) {
            aligned.resize(sampleCount);
            std::memcpy(aligned.data(), bytes, pcmData.length);
            samples = aligned.data();
        }

        jkmeeting::audio::OwnedAudioFrame output;
        const bool accepted = _session->process({.samples = samples,
                                                 .sample_count = sampleCount,
                                                 .sample_rate = _inputSampleRate,
                                                 .channels = _channels,
                                                 .sample_time = sampleTime,
                                                 .host_time_ns = hostTimeNs},
                                                output);
        if (!accepted) {
            assign_error(error, JKMeetingCoreErrorInvalidInput, @"Audio session rejected the PCM frame.");
            return nil;
        }
        return [self frameFrom:output];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for PCM processing.");
        return nil;
    }
}

- (nullable JKMeetingAudioFrame*)finishWithError:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    try {
        jkmeeting::audio::OwnedAudioFrame output;
        if (!_session->finish(output)) {
            assign_error(error, JKMeetingCoreErrorProcessingFailed, @"Audio session has already finished.");
            return nil;
        }
        return [self frameFrom:output];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for PCM processing.");
        return nil;
    }
}

- (JKMeetingAudioSessionStats*)stats {
    return [[JKMeetingAudioSessionStats alloc] initWithStats:_session->stats()];
}

@end

@implementation JKMeetingOggOpusEncoder {
    std::unique_ptr<jkmeeting::audio::OggOpusEncoder> _encoder;
    int _channels;
}

- (nullable instancetype)initWithSampleRate:(NSInteger)sampleRate
                                   channels:(NSInteger)channels
                                 bitrateBps:(NSInteger)bitrateBps
                                     serial:(uint32_t)serial
                                      error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    const NSInteger intMax = std::numeric_limits<int>::max();
    if (sampleRate <= 0 || channels <= 0 || bitrateBps <= 0 || sampleRate > intMax || channels > intMax ||
        bitrateBps > intMax) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid Opus encoder configuration.");
        return nil;
    }
    self = [super init];
    if (self == nil) {
        return nil;
    }
    _channels = static_cast<int>(channels);
    try {
        _encoder = std::make_unique<jkmeeting::audio::OggOpusEncoder>(
            jkmeeting::audio::OpusEncoderConfig{.sample_rate = static_cast<int>(sampleRate),
                                                .channels = _channels,
                                                .bitrate_bps = static_cast<int>(bitrateBps),
                                                .frame_ms = 20},
            serial);
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for Opus encoder.");
        return nil;
    }
    if (!_encoder->valid()) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid Opus encoder configuration.");
        return nil;
    }
    return self;
}

- (NSData*)headerData {
    const std::vector<uint8_t> bytes = _encoder->headers();
    return [NSData dataWithBytes:bytes.data() length:bytes.size()];
}

- (nullable NSData*)encodePCMData:(NSData*)pcmData error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    const std::size_t bytesPerFrame = sizeof(int16_t) * static_cast<std::size_t>(_channels);
    if (pcmData.length == 0 || pcmData.length % bytesPerFrame != 0) {
        assign_error(error, JKMeetingCoreErrorInvalidInput, @"Invalid S16LE PCM frame length.");
        return nil;
    }
    try {
        const std::size_t sampleCount = pcmData.length / sizeof(int16_t);
        std::vector<int16_t> samples(sampleCount);
        std::memcpy(samples.data(), pcmData.bytes, pcmData.length);
        std::vector<uint8_t> bytes;
        if (!_encoder->encode(samples.data(), samples.size(), bytes)) {
            assign_error(error, JKMeetingCoreErrorProcessingFailed, @"Opus encoder failed or has finished.");
            return nil;
        }
        return [NSData dataWithBytes:bytes.data() length:bytes.size()];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for Opus encoding.");
        return nil;
    }
}

- (nullable NSData*)finishWithError:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    try {
        std::vector<uint8_t> bytes;
        if (!_encoder->finish(bytes)) {
            assign_error(error, JKMeetingCoreErrorProcessingFailed,
                         @"Opus encoder cannot finish (no audio, failed, or already finished).");
            return nil;
        }
        return [NSData dataWithBytes:bytes.data() length:bytes.size()];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for Opus encoding.");
        return nil;
    }
}

- (int64_t)durationMs {
    return _encoder->duration_ms();
}

- (int64_t)packetCount {
    return _encoder->packet_count();
}

- (int64_t)bytesWritten {
    return _encoder->bytes_written();
}

@end

@implementation JKMeetingOggOpusDecoder {
    std::unique_ptr<jkmeeting::audio::OggOpusDecoder> _decoder;
}

- (nullable instancetype)init {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    try {
        _decoder = std::make_unique<jkmeeting::audio::OggOpusDecoder>();
    } catch (const std::bad_alloc&) {
        return nil;
    }
    return self;
}

- (nullable NSData*)decodeData:(NSData*)data error:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    try {
        std::vector<int16_t> pcm;
        if (!_decoder->push(static_cast<const uint8_t*>(data.bytes), data.length, pcm)) {
            assign_error(error, JKMeetingCoreErrorProcessingFailed, @"Invalid Ogg Opus stream or decode failure.");
            return nil;
        }
        return [NSData dataWithBytes:pcm.data() length:pcm.size() * sizeof(int16_t)];
    } catch (const std::bad_alloc&) {
        assign_error(error, JKMeetingCoreErrorAllocationFailed, @"Insufficient memory for Opus decoding.");
        return nil;
    }
}

- (BOOL)finishWithError:(NSError* _Nullable * _Nullable)error {
    if (error != nullptr) {
        *error = nil;
    }
    if (!_decoder->finish()) {
        assign_error(error, JKMeetingCoreErrorProcessingFailed, @"Ogg Opus stream is incomplete or invalid.");
        return NO;
    }
    return YES;
}

- (NSInteger)sampleRate {
    return _decoder->sample_rate();
}

- (NSInteger)channels {
    return _decoder->channels();
}

- (int64_t)outputFrames {
    return _decoder->output_frames();
}

- (int64_t)packetCount {
    return _decoder->packet_count();
}

@end
