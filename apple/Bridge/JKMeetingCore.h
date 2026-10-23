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

/// 无效采集样本位置：会话按连续流处理该帧。
FOUNDATION_EXPORT const int64_t JKMeetingInvalidSampleTime;
/// 无效到达时刻：该帧不参与抖动统计。
FOUNDATION_EXPORT const int64_t JKMeetingInvalidHostTime;

@interface JKMeetingAudioSessionStats : NSObject

@property(nonatomic, readonly) int64_t inputFrames;
@property(nonatomic, readonly) int64_t outputFrames;
@property(nonatomic, readonly) int64_t rejectedFrames;
@property(nonatomic, readonly) int64_t gapEvents;
@property(nonatomic, readonly) int64_t filledGapFrames;
@property(nonatomic, readonly) int64_t unfilledGapFrames;
@property(nonatomic, readonly) int64_t overlapFramesDiscarded;
@property(nonatomic, readonly) double jitterMs;
@property(nonatomic, readonly) double maxTransitDeltaMs;
@property(nonatomic, readonly) int64_t speechMs;
@property(nonatomic, readonly) int64_t silenceMs;
@property(nonatomic, readonly) int64_t speechSegments;

@end

/// 一次录音对应一个会话：时间轴 -> 流式重采样 -> 静音检测。非线程安全，调用方需串行访问。
@interface JKMeetingAudioSession : NSObject

- (instancetype)init NS_UNAVAILABLE;

- (nullable instancetype)initWithInputSampleRate:(NSInteger)inputSampleRate
                                        channels:(NSInteger)channels
                                outputSampleRate:(NSInteger)outputSampleRate
                                           error:(NSError* _Nullable * _Nullable)error;

/// 返回的帧可能不含 PCM（重采样尚未积累足够样本），这不是错误。
- (nullable JKMeetingAudioFrame*)processPCMData:(NSData*)pcmData
                                     sampleTime:(int64_t)sampleTime
                                     hostTimeNs:(int64_t)hostTimeNs
                                          error:(NSError* _Nullable * _Nullable)error;

/// 冲刷重采样尾部；之后再调用 process 或 finish 都会失败。
- (nullable JKMeetingAudioFrame*)finishWithError:(NSError* _Nullable * _Nullable)error;

@property(nonatomic, readonly) JKMeetingAudioSessionStats* stats;

@end

/// PCM -> Ogg Opus 字节流（VOIP、VBR、20 ms 帧）。调用顺序：headerData 一次 -> encode 若干次 -> finish 一次。
/// 非线程安全，调用方需串行访问。serial 为 Ogg 逻辑流序号，由调用方随机生成（建议非 0）。
@interface JKMeetingOggOpusEncoder : NSObject

- (instancetype)init NS_UNAVAILABLE;

- (nullable instancetype)initWithSampleRate:(NSInteger)sampleRate
                                   channels:(NSInteger)channels
                                 bitrateBps:(NSInteger)bitrateBps
                                     serial:(uint32_t)serial
                                      error:(NSError* _Nullable * _Nullable)error;

/// OpusHead 页与 OpusTags 页；仅首次调用返回内容。
- (NSData*)headerData;

/// 返回本次封好的页，可能为空（数据仍在当前页中）。失败后编码器不可恢复。
- (nullable NSData*)encodePCMData:(NSData*)pcmData error:(NSError* _Nullable * _Nullable)error;

/// 冲刷编码器并返回含 EOS 的尾页；没有任何输入样本时失败。
- (nullable NSData*)finishWithError:(NSError* _Nullable * _Nullable)error;

/// 由输入样本数推导，不依赖解码器。
@property(nonatomic, readonly) int64_t durationMs;
@property(nonatomic, readonly) int64_t packetCount;
@property(nonatomic, readonly) int64_t bytesWritten;

@end

/// Ogg Opus 字节流 -> 交错 S16LE PCM，按 pre_skip 裁头、按 EOS granule 裁尾。
/// 字节可任意分块喂入；非线程安全，调用方需串行访问。仅支持 1～2 声道（映射族 0）。
@interface JKMeetingOggOpusDecoder : NSObject

/// 输出采样率跟随 OpusHead 中的原始采样率（不受 Opus 支持时为 48 kHz）；内存不足时返回 nil。
/// 不提供 initWithError:：它在 Swift 中导入为 `init() throws`，会被 NSObject 的 `init()` 遮蔽。
- (nullable instancetype)init NS_DESIGNATED_INITIALIZER;

/// 返回本次可输出的 PCM，可能为空（头部或页尚未收全）。输入非法或解码失败后解码器不可恢复。
- (nullable NSData*)decodeData:(NSData*)data error:(NSError* _Nullable * _Nullable)error;

/// 确认流已完整结束（收到 EOS 且无残留字节）。
- (BOOL)finishWithError:(NSError* _Nullable * _Nullable)error;

/// 头部解析前为 0。
@property(nonatomic, readonly) NSInteger sampleRate;
@property(nonatomic, readonly) NSInteger channels;
@property(nonatomic, readonly) int64_t outputFrames;
@property(nonatomic, readonly) int64_t packetCount;

@end

NS_ASSUME_NONNULL_END
