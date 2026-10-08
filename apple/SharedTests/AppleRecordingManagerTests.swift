import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

private final class SimulatedCapture: AudioCapture, @unchecked Sendable {
    private let lock = NSLock()
    private var callback: (@Sendable (CapturedPCMFrame) -> Void)?
    private(set) var stops = 0

    func requestPermission(completion: @escaping @Sendable (MicrophonePermission) -> Void) { completion(.granted) }
    func start(onFrame: @escaping @Sendable (CapturedPCMFrame) -> Void, completion: @escaping @Sendable (Result<Void, AudioCaptureError>) -> Void) {
        lock.withLock { callback = onFrame }
        let samples = [Int16](repeating: 4_000, count: 8_000)
        let pcm = samples.withUnsafeBufferPointer { Data(buffer: $0) }
        onFrame(CapturedPCMFrame(pcmData: pcm, sampleRate: 16_000, channels: 1, timestampMs: 0))
        completion(.success(()))
    }
    func stop() { lock.withLock { stops += 1; callback = nil } }
}

@MainActor
final class AppleRecordingManagerTests: XCTestCase {
    func testDurationLimitFinishesValidRecordingOnlyOnce() async throws {
        let capture = SimulatedCapture()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let manager = AppleRecordingManager(capture: capture, recordingsDirectory: directory, durationLimit: .milliseconds(150))
        let stopped = expectation(description: "到时自动保存录音")
        manager.setAutomaticStopHandler { result, reason in
            XCTAssertEqual(reason, .durationLimit)
            switch result {
            case .success(let metadata):
                XCTAssertGreaterThan(metadata.sizeBytes, 0)
                XCTAssertTrue(FileManager.default.fileExists(atPath: metadata.fileURL.path))
            case .failure(let error): XCTFail("录音不应失败：\(error)")
            }
            stopped.fulfill()
        }
        manager.start { result in
            if case .failure(let error) = result { XCTFail("启动失败：\(error)") }
        }
        await fulfillment(of: [stopped], timeout: 5)
        XCTAssertEqual(capture.stops, 1)
    }

    func testEngineInterruptionPreservesValidRecordingAndDoesNotStopTwice() async throws {
        let capture = SimulatedCapture()
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let manager = AppleRecordingManager(capture: capture, recordingsDirectory: directory)
        let stopped = expectation(description: "中断后保存已录内容")
        manager.setAutomaticStopHandler { result, reason in
            XCTAssertEqual(reason, .interrupted)
            if case .failure(let error) = result { XCTFail("中断有效文件不应失败：\(error)") }
            stopped.fulfill()
        }
        let started = expectation(description: "开始采集并注册中断监听")
        manager.start { result in
            if case .failure(let error) = result { XCTFail("启动失败：\(error)") }
            started.fulfill()
        }
        await fulfillment(of: [started], timeout: 5)
        NotificationCenter.default.post(name: .AVAudioEngineConfigurationChange, object: nil)
        await fulfillment(of: [stopped], timeout: 5)
        manager.stop { result in
            if case .success = result { XCTFail("重复停止不可再次生成录音") }
        }
        XCTAssertEqual(capture.stops, 1)
    }
}
