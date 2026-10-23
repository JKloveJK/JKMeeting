@preconcurrency import AVFoundation
import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class OggOpusFileWriterTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    func testThreeSecondSineDecodesWithMatchingDurations() throws {
        let fileURL = directory.appendingPathComponent("recording.ogg")
        let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)

        // 3 秒正好 150 个 20 ms 包，覆盖 EOS 页边界；按 1024 样本分块模拟会话输出。
        let samples = Self.sine(frequency: 440, sampleRate: 16_000, frames: 48_000)
        var offset = 0
        while offset < samples.count {
            let count = min(1_024, samples.count - offset)
            try writer.append(Self.frame(Array(samples[offset..<offset + count]), timestampMs: Int64(offset / 16)))
            offset += count
        }
        let metadata = try writer.finish()

        XCTAssertEqual(metadata.fileURL, fileURL)
        XCTAssertEqual(metadata.durationMs, 3_000)
        XCTAssertEqual(metadata.sampleRate, 16_000)
        XCTAssertEqual(metadata.channels, 1)
        let attributes = try FileManager.default.attributesOfItem(atPath: fileURL.path)
        XCTAssertEqual((attributes[.size] as? NSNumber)?.int64Value, metadata.sizeBytes)

        let file = try AVAudioFile(forReading: fileURL)
        let fileSeconds = Double(file.length) / file.processingFormat.sampleRate
        let player = try AVAudioPlayer(contentsOf: fileURL)
        print("{\"case\":\"ogg_opus_3s\",\"avaudiofile_s\":\(fileSeconds),\"avaudioplayer_s\":\(player.duration),\"bytes\":\(metadata.sizeBytes)}")
        XCTAssertEqual(fileSeconds, 3.0, accuracy: 0.02, "AVAudioFile.length")
        XCTAssertEqual(player.duration, 3.0, accuracy: 0.02, "AVAudioPlayer.duration")
    }

    func testRejectsMismatchedFrameAndSecondFinish() throws {
        let fileURL = directory.appendingPathComponent("mismatch.ogg")
        let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)

        XCTAssertThrowsError(try writer.append(ProcessedAudioFrame(
            pcmData: Data(count: 640), sampleRate: 48_000, channels: 1, timestampMs: 0
        ))) { XCTAssertEqual($0 as? OggOpusFileWriterError, .invalidPCMFrame) }

        try writer.append(Self.frame(Self.sine(frequency: 300, sampleRate: 16_000, frames: 3_200), timestampMs: 0))
        _ = try writer.finish()
        XCTAssertThrowsError(try writer.finish()) {
            XCTAssertEqual($0 as? OggOpusFileWriterError, .alreadyFinished)
        }
    }

    func testEmptyRecordingFailsAndRemovesFile() throws {
        let fileURL = directory.appendingPathComponent("empty.ogg")
        let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)

        XCTAssertThrowsError(try writer.finish())
        XCTAssertFalse(FileManager.default.fileExists(atPath: fileURL.path))
    }

    func testDiscardRemovesPartialFile() throws {
        let fileURL = directory.appendingPathComponent("discard.ogg")
        let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)
        try writer.append(Self.frame(Self.sine(frequency: 300, sampleRate: 16_000, frames: 1_600), timestampMs: 0))
        XCTAssertTrue(FileManager.default.fileExists(atPath: fileURL.path))

        writer.discard()

        XCTAssertFalse(FileManager.default.fileExists(atPath: fileURL.path))
    }

    func testInvalidConfigurationThrows() {
        let fileURL = directory.appendingPathComponent("invalid.ogg")
        XCTAssertThrowsError(try OggOpusFileWriter(fileURL: fileURL, sampleRate: 44_100, channels: 1)) {
            XCTAssertEqual($0 as? OggOpusFileWriterError, .invalidConfiguration)
        }
        XCTAssertFalse(FileManager.default.fileExists(atPath: fileURL.path))
    }

    private static func sine(frequency: Double, sampleRate: Int, frames: Int) -> [Int16] {
        (0..<frames).map { index in
            Int16(8_000 * sin(2 * Double.pi * frequency * Double(index) / Double(sampleRate)))
        }
    }

    private static func frame(_ samples: [Int16], timestampMs: Int64) -> ProcessedAudioFrame {
        ProcessedAudioFrame(
            pcmData: samples.withUnsafeBufferPointer { Data(buffer: $0) },
            sampleRate: 16_000,
            channels: 1,
            timestampMs: timestampMs
        )
    }
}
