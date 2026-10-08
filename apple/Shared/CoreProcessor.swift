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
        guard error.domain == JKMeetingCoreErrorDomain else {
            return .unexpectedBridgeError
        }

        switch error.code {
        case CoreBridgeErrorCode.invalidInput.rawValue:
            return .invalidPCM
        case CoreBridgeErrorCode.processingFailed.rawValue:
            return .processingFailed
        case CoreBridgeErrorCode.allocationFailed.rawValue:
            return .allocationFailed
        default:
            return .unexpectedBridgeError
        }
    }
}
