import Foundation

public protocol AudioFrameProcessing {
    func resample(
        pcmData: Data,
        sampleRate: Int,
        channels: Int,
        targetSampleRate: Int,
        timestampMs: Int64
    ) throws -> ProcessedAudioFrame
}

extension CoreProcessor: AudioFrameProcessing {}

public protocol ProcessedAudioFrameWriter: AnyObject {
    func append(_ frame: ProcessedAudioFrame) throws
    func finish() throws -> RecordingMetadata
    func discard()
}

public final class RecordingPipeline {
    private let processor: any AudioFrameProcessing
    private let writer: any ProcessedAudioFrameWriter
    private let targetSampleRate: Int

    public init(
        processor: any AudioFrameProcessing = CoreProcessor(),
        writer: any ProcessedAudioFrameWriter,
        targetSampleRate: Int = 16_000
    ) {
        self.processor = processor
        self.writer = writer
        self.targetSampleRate = targetSampleRate
    }

    public func process(_ frame: CapturedPCMFrame) throws {
        do {
            let processed = try processor.resample(
                pcmData: frame.pcmData,
                sampleRate: frame.sampleRate,
                channels: frame.channels,
                targetSampleRate: targetSampleRate,
                timestampMs: frame.timestampMs
            )
            try writer.append(processed)
        } catch {
            writer.discard()
            throw error
        }
    }

    public func finish() throws -> RecordingMetadata {
        try writer.finish()
    }

    public func discard() {
        writer.discard()
    }
}
