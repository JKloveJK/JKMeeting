import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class RecordingPipelineTests: XCTestCase {
    func testProcessCreatesSessionFromFirstFrameAndWritesOutput() throws {
        let processed = ProcessedAudioFrame(pcmData: Data([0, 0, 2, 0]), sampleRate: 16_000, channels: 1, timestampMs: 0)
        let session = FakeSession(outputs: [processed])
        let writer = FakeWriter()
        var factoryCalls: [[Int]] = []
        let pipeline = RecordingPipeline(writer: writer) { inputRate, channels, outputRate in
            factoryCalls.append([inputRate, channels, outputRate])
            return session
        }

        let frame = CapturedPCMFrame(pcmData: Data([0, 0, 1, 0]), sampleRate: 48_000, channels: 1, timestampMs: 0, sampleTime: 960)
        try pipeline.process(frame)
        try pipeline.process(frame)

        XCTAssertEqual(factoryCalls, [[48_000, 1, 16_000]])
        XCTAssertEqual(session.received.map(\.sampleTime), [960, 960])
        XCTAssertEqual(writer.frames, [processed])
    }

    func testFinishWritesSessionTailAndExposesStats() throws {
        let tail = ProcessedAudioFrame(pcmData: Data([4, 0]), sampleRate: 16_000, channels: 1, timestampMs: 10)
        var stats = AudioSessionStats()
        stats.gapEvents = 2
        stats.speechMs = 1_200
        let session = FakeSession(outputs: [], tail: tail, stats: stats)
        let metadata = RecordingMetadata(
            fileURL: URL(fileURLWithPath: "/tmp/recording.m4a"), durationMs: 1_000, sizeBytes: 128, sampleRate: 16_000, channels: 1
        )
        let writer = FakeWriter(metadata: metadata)
        let pipeline = RecordingPipeline(writer: writer) { _, _, _ in session }

        try pipeline.process(CapturedPCMFrame(pcmData: Data([0, 0]), sampleRate: 48_000, channels: 1, timestampMs: 0))

        XCTAssertEqual(try pipeline.finish(), metadata)
        XCTAssertTrue(session.didFinish)
        XCTAssertEqual(writer.frames, [tail])
        XCTAssertTrue(writer.didFinish)
        XCTAssertEqual(pipeline.lastSessionStats, stats)
    }

    func testFinishWithoutFramesOnlyFinishesWriter() throws {
        let writer = FakeWriter()
        let pipeline = RecordingPipeline(writer: writer) { _, _, _ in
            XCTFail("没有采集帧时不应创建会话")
            return FakeSession(outputs: [])
        }

        _ = try pipeline.finish()

        XCTAssertTrue(writer.didFinish)
        XCTAssertNil(pipeline.lastSessionStats)
    }

    func testProcessFailureDiscardsWriter() {
        let writer = FakeWriter()
        let pipeline = RecordingPipeline(writer: writer) { _, _, _ in FakeSession(outputs: [], error: .invalidPCM) }

        XCTAssertThrowsError(try pipeline.process(CapturedPCMFrame(
            pcmData: Data([0]), sampleRate: 16_000, channels: 1, timestampMs: 0
        )))
        XCTAssertTrue(writer.didDiscard)
    }

    func testCoreSessionFillsDroppedFramesToKeepRealDuration() throws {
        let writer = FakeWriter()
        let pipeline = RecordingPipeline(writer: writer)
        let sourceRate = 48_000
        let chunk = 1_024
        let totalFrames = sourceRate * 3
        let chunkData = Data(count: chunk * MemoryLayout<Int16>.size)

        var dropped: Int64 = 0
        var index = 0
        var offset = 0
        while offset < totalFrames {
            let count = min(chunk, totalFrames - offset)
            let isLast = offset + count == totalFrames
            if index % 10 == 9, !isLast {
                dropped += 1
            } else {
                try pipeline.process(CapturedPCMFrame(
                    pcmData: chunkData.prefix(count * MemoryLayout<Int16>.size),
                    sampleRate: sourceRate,
                    channels: 1,
                    timestampMs: 0,
                    sampleTime: Int64(offset)
                ))
            }
            offset += count
            index += 1
        }
        _ = try pipeline.finish()

        let writtenSamples = writer.frames.reduce(0) { $0 + $1.pcmData.count / MemoryLayout<Int16>.size }
        XCTAssertEqual(writtenSamples, totalFrames / 3, accuracy: 1)
        XCTAssertEqual(pipeline.lastSessionStats?.gapEvents, dropped)
        XCTAssertEqual(pipeline.lastSessionStats?.filledGapFrames, dropped * Int64(chunk))
    }

    func testThirtySecondCoreProcessingRatioBaseline() throws {
        let tempDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: tempDir) }

        let sourceSampleRate = 48_000
        let durationSeconds = 30
        let totalSamples = sourceSampleRate * durationSeconds
        let chunkSamples = 1_024

        var chunkBuffer = [Int16](repeating: 0, count: chunkSamples)
        for idx in 0..<chunkSamples {
            let phase = Double(idx) * 2.0 * Double.pi * 440.0 / Double(sourceSampleRate)
            chunkBuffer[idx] = Int16(sin(phase) * 8_000.0)
        }
        let chunkData = chunkBuffer.withUnsafeBufferPointer { Data(buffer: $0) }

        let sampleCount = 20
        var coreMsSamples: [Double] = []
        var ratioSamples: [Double] = []
        let captureDurationMs = Double(durationSeconds * 1_000)

        for run in 1...sampleCount {
            let fileURL = tempDir.appendingPathComponent("baseline-30s-\(run).ogg")
            let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)
            let pipeline = RecordingPipeline(writer: writer, targetSampleRate: 16_000)

            var offset = 0
            while offset < totalSamples {
                let remaining = min(chunkSamples, totalSamples - offset)
                let data = remaining == chunkSamples ? chunkData : chunkData.prefix(remaining * MemoryLayout<Int16>.size)
                let timestampMs = Int64((Double(offset) / Double(sourceSampleRate)) * 1_000.0)
                try pipeline.process(CapturedPCMFrame(
                    pcmData: data,
                    sampleRate: sourceSampleRate,
                    channels: 1,
                    timestampMs: timestampMs,
                    sampleTime: Int64(offset)
                ))
                offset += remaining
            }

            let metadata = try pipeline.finish()
            let coreMs = pipeline.coreProcessingDurationMs
            let ratioPercent = (coreMs / captureDurationMs) * 100.0
            coreMsSamples.append(coreMs)
            ratioSamples.append(ratioPercent)

            print(String(
                format: "P2_2_CORE_SAMPLE run=%d capture_ms=%.1f core_ms=%.3f ratio_pct=%.5f output_duration_ms=%d output_bytes=%lld",
                run,
                captureDurationMs,
                coreMs,
                ratioPercent,
                metadata.durationMs,
                metadata.sizeBytes
            ))

            XCTAssertGreaterThan(pipeline.coreProcessingNanoseconds, 0)
            XCTAssertLessThan(ratioPercent, 20.0, "会话加 Opus 编码的耗时占比应显著低于 20% 阈值")
            XCTAssertEqual(metadata.durationMs, durationSeconds * 1_000)
            XCTAssertEqual(pipeline.lastSessionStats?.outputFrames, Int64(totalSamples / 3))
        }

        let sortedCore = coreMsSamples.sorted()
        let sortedRatio = ratioSamples.sorted()
        func percentile(_ sorted: [Double], _ p: Double) -> Double {
            let idx = min(sorted.count - 1, max(0, Int(ceil(p * Double(sorted.count))) - 1))
            return sorted[idx]
        }

        print(String(
            format: "P2_2_CORE_SUMMARY samples=%d core_ms_p50=%.3f core_ms_p95=%.3f core_ms_p99=%.3f ratio_pct_p50=%.5f ratio_pct_p95=%.5f ratio_pct_p99=%.5f",
            sampleCount,
            percentile(sortedCore, 0.50),
            percentile(sortedCore, 0.95),
            percentile(sortedCore, 0.99),
            percentile(sortedRatio, 0.50),
            percentile(sortedRatio, 0.95),
            percentile(sortedRatio, 0.99)
        ))
    }
}

private final class FakeSession: AudioSessionProcessing {
    private var outputs: [ProcessedAudioFrame]
    private let tail: ProcessedAudioFrame
    private let error: CoreProcessorError?
    let stats: AudioSessionStats
    var received: [CapturedPCMFrame] = []
    var didFinish = false

    init(
        outputs: [ProcessedAudioFrame],
        tail: ProcessedAudioFrame = ProcessedAudioFrame(pcmData: Data(), sampleRate: 16_000, channels: 1, timestampMs: 0),
        stats: AudioSessionStats = AudioSessionStats(),
        error: CoreProcessorError? = nil
    ) {
        self.outputs = outputs
        self.tail = tail
        self.stats = stats
        self.error = error
    }

    func process(_ frame: CapturedPCMFrame) throws -> ProcessedAudioFrame {
        received.append(frame)
        if let error { throw error }
        if outputs.isEmpty {
            return ProcessedAudioFrame(pcmData: Data(), sampleRate: 16_000, channels: frame.channels, timestampMs: 0)
        }
        return outputs.removeFirst()
    }

    func finish() throws -> ProcessedAudioFrame {
        didFinish = true
        return tail
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
