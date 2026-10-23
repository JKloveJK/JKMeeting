import Foundation

public struct ProcessedAudioFrame: Equatable {
    public let pcmData: Data
    public let sampleRate: Int
    public let channels: Int
    public let timestampMs: Int64

    public init(pcmData: Data, sampleRate: Int, channels: Int, timestampMs: Int64) {
        self.pcmData = pcmData
        self.sampleRate = sampleRate
        self.channels = channels
        self.timestampMs = timestampMs
    }

    init(frame: JKMeetingAudioFrame) {
        self.init(
            pcmData: frame.pcmData,
            sampleRate: frame.sampleRate,
            channels: frame.channels,
            timestampMs: frame.timestampMs
        )
    }
}

public enum CoreProcessorError: Error, Equatable {
    case invalidPCM
    case processingFailed
    case allocationFailed
    case unexpectedBridgeError
}

public struct CoreProcessor {
    public init() {}

    public func resample(
        pcmData: Data,
        sampleRate: Int,
        channels: Int,
        targetSampleRate: Int,
        timestampMs: Int64
    ) throws -> ProcessedAudioFrame {
        do {
            let frame = try JKMeetingCore().resamplePCMData(
                pcmData,
                sampleRate: sampleRate,
                channels: channels,
                targetSampleRate: targetSampleRate,
                timestampMs: timestampMs
            )
            return ProcessedAudioFrame(frame: frame)
        } catch let error as NSError {
            throw map(error)
        }
    }

    private func map(_ error: NSError) -> CoreProcessorError {
        CoreProcessorError(bridgeError: error)
    }
}

extension CoreProcessorError {
    init(bridgeError error: NSError) {
        guard error.domain == JKMeetingCoreErrorDomain else {
            self = .unexpectedBridgeError
            return
        }

        switch error.code {
        case CoreBridgeErrorCode.invalidInput.rawValue:
            self = .invalidPCM
        case CoreBridgeErrorCode.processingFailed.rawValue:
            self = .processingFailed
        case CoreBridgeErrorCode.allocationFailed.rawValue:
            self = .allocationFailed
        default:
            self = .unexpectedBridgeError
        }
    }
}

public struct AudioSessionStats: Equatable, Sendable {
    public var inputFrames: Int64 = 0
    public var outputFrames: Int64 = 0
    public var rejectedFrames: Int64 = 0
    public var gapEvents: Int64 = 0
    public var filledGapFrames: Int64 = 0
    public var unfilledGapFrames: Int64 = 0
    public var overlapFramesDiscarded: Int64 = 0
    public var jitterMs: Double = 0
    public var maxTransitDeltaMs: Double = 0
    public var speechMs: Int64 = 0
    public var silenceMs: Int64 = 0
    public var speechSegments: Int64 = 0

    public init() {}

    init(_ stats: JKMeetingAudioSessionStats) {
        inputFrames = stats.inputFrames
        outputFrames = stats.outputFrames
        rejectedFrames = stats.rejectedFrames
        gapEvents = stats.gapEvents
        filledGapFrames = stats.filledGapFrames
        unfilledGapFrames = stats.unfilledGapFrames
        overlapFramesDiscarded = stats.overlapFramesDiscarded
        jitterMs = stats.jitterMs
        maxTransitDeltaMs = stats.maxTransitDeltaMs
        speechMs = stats.speechMs
        silenceMs = stats.silenceMs
        speechSegments = stats.speechSegments
    }
}

/// 一次录音的有状态处理会话：时间轴、流式重采样与静音检测。调用方需串行访问。
public protocol AudioSessionProcessing: AnyObject {
    func process(_ frame: CapturedPCMFrame) throws -> ProcessedAudioFrame
    func finish() throws -> ProcessedAudioFrame
    var stats: AudioSessionStats { get }
}

public final class CoreAudioSession: AudioSessionProcessing {
    private let session: JKMeetingAudioSession

    public init(inputSampleRate: Int, channels: Int, outputSampleRate: Int) throws {
        do {
            session = try JKMeetingAudioSession(
                inputSampleRate: inputSampleRate,
                channels: channels,
                outputSampleRate: outputSampleRate
            )
        } catch let error as NSError {
            throw CoreProcessorError(bridgeError: error)
        }
    }

    public func process(_ frame: CapturedPCMFrame) throws -> ProcessedAudioFrame {
        do {
            let output = try session.processPCMData(
                frame.pcmData,
                sampleTime: frame.sampleTime ?? JKMeetingInvalidSampleTime,
                hostTimeNs: frame.hostTimeNs ?? JKMeetingInvalidHostTime
            )
            return ProcessedAudioFrame(frame: output)
        } catch let error as NSError {
            throw CoreProcessorError(bridgeError: error)
        }
    }

    public func finish() throws -> ProcessedAudioFrame {
        do {
            return ProcessedAudioFrame(frame: try session.finish())
        } catch let error as NSError {
            throw CoreProcessorError(bridgeError: error)
        }
    }

    public var stats: AudioSessionStats {
        AudioSessionStats(session.stats)
    }
}
