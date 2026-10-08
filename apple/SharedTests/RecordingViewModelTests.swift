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
