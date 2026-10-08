import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class RecordingStateTests: XCTestCase {
    func testPermissionToRecordedFollowsLegalTransitions() throws {
        var state = RecordingState.idle
        state = try RecordingStateReducer.reduce(state, event: .requestStart)
        state = try RecordingStateReducer.reduce(state, event: .permissionGranted)
        state = try RecordingStateReducer.reduce(state, event: .captureStarted)
        state = try RecordingStateReducer.reduce(state, event: .requestStop)
        state = try RecordingStateReducer.reduce(state, event: .fileValidated(metadata()))

        XCTAssertEqual(state, .recorded(metadata(), stopReason: .user))
    }

    func testInterruptedValidFileBecomesRecordedWithVisibleReason() throws {
        let state = try RecordingStateReducer.reduce(
            .recording,
            event: .captureStopped(reason: .interrupted, metadata: metadata())
        )

        XCTAssertEqual(state, .recorded(metadata(), stopReason: .interrupted))
    }

    func testDurationLimitInvalidFileFailsWithVisibleReason() throws {
        let state = try RecordingStateReducer.reduce(
            .recording,
            event: .fileValidationFailed(reason: .durationLimit, message: "录音文件无效")
        )

        XCTAssertEqual(state, .failed(.invalidRecordedFile, message: "录音文件无效"))
    }

    func testRecordedCanEnterAndLeavePlayback() throws {
        let initial = RecordingState.recorded(metadata(), stopReason: .user)
        let playing = try RecordingStateReducer.reduce(initial, event: .playbackStarted)
        let stopped = try RecordingStateReducer.reduce(playing, event: .playbackStopped)

        XCTAssertEqual(stopped, initial)
    }

    private func metadata() -> RecordingMetadata {
        RecordingMetadata(
            fileURL: URL(fileURLWithPath: "/tmp/recording.m4a"),
            durationMs: 1_000,
            sizeBytes: 128,
            sampleRate: 16_000,
            channels: 1
        )
    }
}
