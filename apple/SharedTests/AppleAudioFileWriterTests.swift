import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class AppleAudioFileWriterTests: XCTestCase {
    func testWriterCreatesReadableM4AAndReturnsMetadata() throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let fileURL = directory.appendingPathComponent("recording.m4a")
        let writer = try AppleAudioFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)

        try writer.append(ProcessedAudioFrame(
            pcmData: Data(repeating: 0, count: 320),
            sampleRate: 16_000,
            channels: 1,
            timestampMs: 0
        ))
        let metadata = try writer.finish()

        XCTAssertEqual(metadata.fileURL, fileURL)
        XCTAssertGreaterThan(metadata.sizeBytes, 0)
        XCTAssertEqual(metadata.sampleRate, 16_000)
        XCTAssertEqual(metadata.channels, 1)
    }

    func testWriterDiscardsPartialFileAfterFailure() throws {
        let fileURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathExtension("m4a")
        let writer = try AppleAudioFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)

        writer.discard()

        XCTAssertFalse(FileManager.default.fileExists(atPath: fileURL.path))
    }
}
