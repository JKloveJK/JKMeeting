@preconcurrency import AVFoundation
import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class OggOpusFilePlayerTests: XCTestCase {
    private var directory: URL!

    override func setUpWithError() throws {
        directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: directory)
    }

    func testPlaysToEndWithMatchingDurationAndNoUnderrun() throws {
        // 2.5 秒超过一个 4 KiB 读取块，覆盖“预缓冲 + 后台续解码”两段路径。
        let fileURL = try writeRecording(name: "full.ogg", frames: 40_000)
        let player = OggOpusFilePlayer()
        let finished = expectation(description: "finished")
        let box = MetricsBox()
        try player.play(fileURL: fileURL, onFinish: { result in
            if case .success(let metrics) = result { box.value = metrics }
            finished.fulfill()
        })
        wait(for: [finished], timeout: 10)

        let metrics = try XCTUnwrap(box.value)
        print("P2_3_PLAYBACK_FULL {\"decoded\":\(metrics.decodedFrames),\"rendered\":\(metrics.renderedFrames),\"underruns\":\(metrics.underrunCallbacks),\"first_frame_ms\":\(metrics.firstFrameLatencyMs),\"output_latency_ms\":\(metrics.outputLatencyMs)}")
        XCTAssertEqual(metrics.sampleRate, 16_000)
        XCTAssertEqual(metrics.channels, 1)
        XCTAssertEqual(metrics.decodedFrames, 40_000)
        XCTAssertEqual(metrics.renderedFrames, metrics.decodedFrames)
        XCTAssertEqual(Double(metrics.renderedFrames) / 16_000, 2.5, accuracy: 0.02)
        XCTAssertEqual(metrics.underrunCallbacks, 0)
        XCTAssertGreaterThan(metrics.firstFrameLatencyMs, 0)
    }

    func testFirstFrameLatencyBaseline() throws {
        let fileURL = try writeRecording(name: "first-frame.ogg", frames: 16_000)
        var firstFrame = [Double]()
        var audible = [Double]()
        for _ in 0..<20 {
            let player = OggOpusFilePlayer()
            let started = expectation(description: "first frame")
            let box = MetricsBox()
            try player.play(fileURL: fileURL, onFirstFrame: { metrics in
                box.value = metrics
                player.stop()
                started.fulfill()
            })
            wait(for: [started], timeout: 5)
            let metrics = try XCTUnwrap(box.value)
            firstFrame.append(metrics.firstFrameLatencyMs)
            audible.append(metrics.audibleFirstFrameLatencyMs)
        }
        let summary = String(
            format: "{\"runs\":20,\"first_frame_p50_ms\":%.2f,\"first_frame_p95_ms\":%.2f,\"audible_p50_ms\":%.2f,\"audible_p95_ms\":%.2f}",
            Self.percentile(firstFrame, 0.5), Self.percentile(firstFrame, 0.95),
            Self.percentile(audible, 0.5), Self.percentile(audible, 0.95)
        )
        print("P2_3_FIRST_FRAME \(summary)")
        XCTAssertLessThan(Self.percentile(audible, 0.95), 200, "C6 目标：可闻首帧 P95 < 200 ms")
    }

    func testRejectsMissingAndCorruptedFiles() throws {
        let player = OggOpusFilePlayer()
        XCTAssertThrowsError(try player.play(fileURL: directory.appendingPathComponent("missing.ogg"))) {
            XCTAssertEqual($0 as? OggOpusPlaybackError, .unreadableFile)
        }
        let garbage = directory.appendingPathComponent("garbage.ogg")
        try Data(repeating: 0x42, count: 4_096).write(to: garbage)
        XCTAssertThrowsError(try player.play(fileURL: garbage)) {
            XCTAssertEqual($0 as? OggOpusPlaybackError, .invalidStream)
        }
    }

    @MainActor
    func testPlaybackRoutesOggToCorePlayerAndKeepsM4A() throws {
        let playback = AppleAudioPlayback()
        let oggURL = try writeRecording(name: "route.ogg", frames: 8_000)
        let oggStarted = expectation(description: "ogg started")
        playback.play(fileURL: oggURL) { result in
            if case .success = result { oggStarted.fulfill() }
        }
        wait(for: [oggStarted], timeout: 5)
        let deadline = Date().addingTimeInterval(5)
        while playback.lastPlaybackMetrics == nil && Date() < deadline {
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
        }
        XCTAssertNotNil(playback.lastPlaybackMetrics, "ogg 应走 core 播放器并产生首帧指标")
        playback.stop()

        let m4aURL = directory.appendingPathComponent("legacy.m4a")
        let writer = try AppleAudioFileWriter(fileURL: m4aURL, sampleRate: 16_000, channels: 1)
        try writer.append(Self.frame(Self.sine(frames: 8_000)))
        _ = try writer.finish()
        let m4aStarted = expectation(description: "m4a started")
        playback.play(fileURL: m4aURL) { result in
            if case .success = result { m4aStarted.fulfill() }
        }
        wait(for: [m4aStarted], timeout: 5)
        playback.stop()
    }

    private func writeRecording(name: String, frames: Int) throws -> URL {
        let fileURL = directory.appendingPathComponent(name)
        let writer = try OggOpusFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)
        let samples = Self.sine(frames: frames)
        var offset = 0
        while offset < samples.count {
            let count = min(1_024, samples.count - offset)
            try writer.append(Self.frame(Array(samples[offset..<offset + count])))
            offset += count
        }
        _ = try writer.finish()
        return fileURL
    }

    private static func sine(frames: Int) -> [Int16] {
        (0..<frames).map { Int16(6_000 * sin(2 * Double.pi * 440 * Double($0) / 16_000)) }
    }

    private static func frame(_ samples: [Int16]) -> ProcessedAudioFrame {
        ProcessedAudioFrame(
            pcmData: samples.withUnsafeBufferPointer { Data(buffer: $0) },
            sampleRate: 16_000,
            channels: 1,
            timestampMs: 0
        )
    }

    private static func percentile(_ values: [Double], _ p: Double) -> Double {
        let sorted = values.sorted()
        let index = max(0, Int((p * Double(sorted.count)).rounded(.up)) - 1)
        return sorted[min(index, sorted.count - 1)]
    }
}

private final class MetricsBox: @unchecked Sendable {
    var value: PlaybackMetrics?
}

final class PCMRingBufferTests: XCTestCase {
    func testWrapsAroundAndRespectsCapacity() {
        let ring = PCMRingBuffer(capacity: 8)
        let first: [Float] = [1, 2, 3, 4, 5, 6]
        XCTAssertEqual(first.withUnsafeBufferPointer { ring.write($0) }, 6)
        var out = [Float](repeating: 0, count: 8)
        XCTAssertEqual(out.withUnsafeMutableBufferPointer { ring.read(into: $0.baseAddress!, count: 4) }, 4)
        XCTAssertEqual(Array(out[0..<4]), [1, 2, 3, 4])

        let second: [Float] = [7, 8, 9, 10, 11, 12, 13]  // 只剩 6 个空位，并跨越尾部回绕
        XCTAssertEqual(second.withUnsafeBufferPointer { ring.write($0) }, 6)
        XCTAssertEqual(ring.availableToRead, 8)
        XCTAssertEqual(ring.availableToWrite, 0)
        XCTAssertEqual(out.withUnsafeMutableBufferPointer { ring.read(into: $0.baseAddress!, count: 8) }, 8)
        XCTAssertEqual(out, [5, 6, 7, 8, 9, 10, 11, 12])
        XCTAssertEqual(out.withUnsafeMutableBufferPointer { ring.read(into: $0.baseAddress!, count: 8) }, 0)
    }
}
