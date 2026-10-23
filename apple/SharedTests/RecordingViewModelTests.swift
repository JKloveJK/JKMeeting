import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

@MainActor
final class RecordingViewModelTests: XCTestCase {
    func testStartAndStopExposeRecordingAndPlaybackState() async {
        let metadata = RecordingMetadata(
            fileURL: URL(fileURLWithPath: "/tmp/recording.m4a"),
            durationMs: 1_000,
            sizeBytes: 128,
            sampleRate: 16_000,
            channels: 1
        )
        let manager = FakeRecordingManager(metadata: metadata)
        let viewModel = RecordingViewModel(manager: manager)

        viewModel.startRecording()
        await Task.yield()
        XCTAssertEqual(viewModel.state, .recording)

        viewModel.stopRecording()
        await Task.yield()
        XCTAssertEqual(viewModel.state, .recorded(metadata, stopReason: .user))

        viewModel.playRecording()
        await Task.yield()
        XCTAssertEqual(viewModel.state, .playing(metadata, stopReason: .user))
    }

    func testTranscriptionIsOptInAndShowsReturnedText() async throws {
        let metadata = RecordingMetadata(fileURL: URL(fileURLWithPath: "/tmp/recording.m4a"), durationMs: 1_000, sizeBytes: 128, sampleRate: 16_000, channels: 1)
        let client = FakeTranscriptionClient()
        let viewModel = RecordingViewModel(manager: FakeRecordingManager(metadata: metadata), transcriptionClient: client)
        viewModel.startRecording()
        await Task.yield()
        viewModel.stopRecording()
        await Task.yield()
        XCTAssertEqual(client.calls, 0)
        viewModel.startTranscription()
        for _ in 0..<20 where viewModel.transcriptionText == nil { await Task.yield() }
        XCTAssertEqual(client.calls, 1)
        XCTAssertEqual(viewModel.transcriptionText, "已识别的文字")
    }

    func testRemotePlaybackPollsUntilHLSReadyAfterTranscription() async throws {
        let metadata = RecordingMetadata(fileURL: URL(fileURLWithPath: "/tmp/recording.ogg"), durationMs: 1_000, sizeBytes: 128, sampleRate: 16_000, channels: 1)
        let client = FakeRemotePlaybackClient(statuses: ["pending", "processing", "ready"])
        let viewModel = RecordingViewModel(manager: FakeRecordingManager(metadata: metadata), transcriptionClient: client,
                                           remotePlaybackPollInterval: .milliseconds(10))
        viewModel.startRecording()
        await Task.yield()
        viewModel.stopRecording()
        await Task.yield()
        XCTAssertNil(viewModel.remotePlayback)

        viewModel.startTranscription()
        let deadline = Date().addingTimeInterval(2)
        while viewModel.remotePlayback?.isHLSReady != true, Date() < deadline {
            try await Task.sleep(for: .milliseconds(5))
        }
        XCTAssertEqual(viewModel.remotePlayback?.isHLSReady, true)
        XCTAssertEqual(client.playbackCalls, 3)
        XCTAssertEqual(viewModel.remotePlayback?.variantsKbps, [32, 64, 96])

        viewModel.startRecording()
        XCTAssertNil(viewModel.remotePlayback, "重新录音须清空上一条录音的远程回放")
    }
}

@MainActor
private final class FakeRemotePlaybackClient: RecordingTranscribing, RemotePlaybackProviding {
    private var statuses: [String]
    private(set) var playbackCalls = 0
    private(set) var lastRecordingID: String?

    init(statuses: [String]) { self.statuses = statuses }

    func transcribe(_ metadata: RecordingMetadata, onStatus: @escaping @MainActor (TranscriptionPhase) -> Void) async throws -> String {
        lastRecordingID = "8fc71433-a413-4975-8dcc-df9e21ab737f"
        return "文字"
    }

    func remotePlayback(recordingID: String) async throws -> RemotePlayback {
        playbackCalls += 1
        let status = statuses.count > 1 ? statuses.removeFirst() : statuses[0]
        let ready = status == "ready"
        return RemotePlayback(recordingID: recordingID, transcodeStatus: status,
                              hlsURL: ready ? URL(string: "http://127.0.0.1:8082/v1/hls/\(recordingID)/master.m3u8?sig=x") : nil,
                              variantsKbps: ready ? [32, 64, 96] : [], transcodeError: nil)
    }
}

@MainActor
private final class FakeTranscriptionClient: RecordingTranscribing {
    var calls = 0
    func transcribe(_ metadata: RecordingMetadata, onStatus: @escaping @MainActor (TranscriptionPhase) -> Void) async throws -> String {
        calls += 1
        onStatus(.transcribing)
        return "已识别的文字"
    }
}

@MainActor
private final class FakeRecordingManager: RecordingManaging {
    let metadata: RecordingMetadata

    init(metadata: RecordingMetadata) { self.metadata = metadata }
    func start(completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) { completion(.success(())) }
    func stop(completion: @escaping @Sendable (Result<RecordingMetadata, RecordingError>) -> Void) { completion(.success(metadata)) }
    func play(fileURL: URL, completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) { completion(.success(())) }
    func stopPlayback() {}
}
