#import <Foundation/Foundation.h>

NS_ASSUME_NONNULL_BEGIN

FOUNDATION_EXPORT NSErrorDomain const JKMeetingCoreErrorDomain;

typedef NS_ENUM(NSInteger, JKMeetingCoreErrorCode) {
    JKMeetingCoreErrorInvalidInput = 1,
    JKMeetingCoreErrorProcessingFailed = 2,
    JKMeetingCoreErrorAllocationFailed = 3,
} NS_SWIFT_NAME(CoreBridgeErrorCode);

@interface JKMeetingAudioFrame : NSObject

@property(nonatomic, readonly) NSData* pcmData;
@property(nonatomic, readonly) NSInteger sampleRate;
@property(nonatomic, readonly) NSInteger channels;
@property(nonatomic, readonly) int64_t timestampMs;

@end

@interface JKMeetingCore : NSObject

- (nullable JKMeetingAudioFrame*)resamplePCMData:(NSData*)pcmData
                                      sampleRate:(NSInteger)sampleRate
                                        channels:(NSInteger)channels
                                targetSampleRate:(NSInteger)targetSampleRate
                                     timestampMs:(int64_t)timestampMs
                                           error:(NSError* _Nullable * _Nullable)error;

@end

NS_ASSUME_NONNULL_END
