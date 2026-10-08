import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class RecordingPipelineTests: XCTestCase {
    func testProcessSendsCoreOutputToWriter() throws {
        let processed = ProcessedAudioFrame(
            pcmData: Data([0, 0, 2, 0]),
            sampleRate: 16_000,
            channels: 1,
            timestampMs: 42
        )
        let processor = FakeProcessor(result: processed)
        let writer = FakeWriter()
        let pipeline = RecordingPipeline(processor: processor, writer: writer)

        try pipeline.process(CapturedPCMFrame(
            pcmData: Data([0, 0, 1, 0]),
            sampleRate: 48_000,
            channels: 1,
            timestampMs: 42
        ))

        XCTAssertEqual(processor.receivedSampleRate, 48_000)
        XCTAssertEqual(processor.receivedTargetSampleRate, 16_000)
        XCTAssertEqual(writer.frames, [processed])
    }

    func testFinishReturnsWriterMetadata() throws {
        let metadata = RecordingMetadata(
            fileURL: URL(fileURLWithPath: "/tmp/recording.m4a"),
            durationMs: 1_000,
            sizeBytes: 128,
            sampleRate: 16_000,
            channels: 1
        )
        let writer = FakeWriter(metadata: metadata)
        let pipeline = RecordingPipeline(processor: FakeProcessor(), writer: writer)

        XCTAssertEqual(try pipeline.finish(), metadata)
        XCTAssertTrue(writer.didFinish)
    }

    func testProcessFailureDiscardsWriter() {
        let writer = FakeWriter()
        let pipeline = RecordingPipeline(processor: FakeProcessor(error: .invalidPCM), writer: writer)

        XCTAssertThrowsError(try pipeline.process(CapturedPCMFrame(
            pcmData: Data([0]), sampleRate: 16_000, channels: 1, timestampMs: 0
        )))
        XCTAssertTrue(writer.didDiscard)
    }
}

private final class FakeProcessor: AudioFrameProcessing {
    var result: ProcessedAudioFrame?
    var error: CoreProcessorError?
    var receivedSampleRate: Int?
    var receivedTargetSampleRate: Int?

    init(result: ProcessedAudioFrame? = nil, error: CoreProcessorError? = nil) {
        self.result = result
        self.error = error
    }

    func resample(pcmData: Data, sampleRate: Int, channels: Int, targetSampleRate: Int, timestampMs: Int64) throws -> ProcessedAudioFrame {
        receivedSampleRate = sampleRate
        receivedTargetSampleRate = targetSampleRate
        if let error { throw error }
        return result ?? ProcessedAudioFrame(pcmData: pcmData, sampleRate: targetSampleRate, channels: channels, timestampMs: timestampMs)
    }
}

private final class FakeWriter: ProcessedAudioFrameWriter {
    var frames: [ProcessedAudioFrame] = []
    var didFinish = false
    var didDiscard = false
    let metadata: RecordingMetadata

    init(metadata: RecordingMetadata = RecordingMetadata(fileURL: URL(fileURLWithPath: "/tmp/default.m4a"), durationMs: 1, sizeBytes: 1, sampleRate: 16_000, channels: 1)) {
        self.metadata = metadata
    }

    func append(_ frame: ProcessedAudioFrame) throws { frames.append(frame) }
    func finish() throws -> RecordingMetadata { didFinish = true; return metadata }
    func discard() { didDiscard = true }
}
