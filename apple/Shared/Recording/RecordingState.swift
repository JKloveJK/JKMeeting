import Foundation

public struct RecordingMetadata: Equatable, Sendable {
    public let fileURL: URL
    public let durationMs: Int
    public let sizeBytes: Int64
    public let sampleRate: Int
    public let channels: Int

    public init(fileURL: URL, durationMs: Int, sizeBytes: Int64, sampleRate: Int, channels: Int) {
        self.fileURL = fileURL
        self.durationMs = durationMs
        self.sizeBytes = sizeBytes
        self.sampleRate = sampleRate
        self.channels = channels
    }
}

public enum RecordingStopReason: Equatable, Sendable {
    case user
    case interrupted
    case durationLimit
}

public enum RecordingError: Error, Equatable, Sendable {
    case permissionDenied
    case captureFailed
    case invalidRecordedFile
    case playbackFailed
    case illegalTransition
}

public enum RecordingState: Equatable, Sendable {
    case idle
    case requestingPermission
    case startingCapture
    case recording
    case stopping
    case recorded(RecordingMetadata, stopReason: RecordingStopReason)
    case playing(RecordingMetadata, stopReason: RecordingStopReason)
    case failed(RecordingError, message: String)
}

public enum RecordingEvent: Equatable, Sendable {
    case requestStart
    case permissionGranted
    case permissionDenied
    case captureStarted
    case requestStop
    case captureStopped(reason: RecordingStopReason, metadata: RecordingMetadata)
    case fileValidated(RecordingMetadata)
    case fileValidationFailed(reason: RecordingStopReason, message: String)
    case playbackStarted
    case playbackStopped
    case playbackFailed(message: String)
}

public enum RecordingStateReducer {
    public static func reduce(_ state: RecordingState, event: RecordingEvent) throws -> RecordingState {
        switch (state, event) {
        case (.idle, .requestStart):
            return .requestingPermission
        case (.requestingPermission, .permissionGranted):
            return .startingCapture
        case (.requestingPermission, .permissionDenied):
            return .failed(.permissionDenied, message: "麦克风权限被拒绝")
        case (.startingCapture, .captureStarted):
            return .recording
        case (.recording, .requestStop):
            return .stopping
        case (.stopping, .fileValidated(let metadata)):
            return .recorded(metadata, stopReason: .user)
        case (.recording, .captureStopped(let reason, let metadata)),
             (.stopping, .captureStopped(let reason, let metadata)):
            return .recorded(metadata, stopReason: reason)
        case (.recording, .fileValidationFailed(_, let message)),
             (.stopping, .fileValidationFailed(_, let message)):
            return .failed(.invalidRecordedFile, message: message)
        case (.recorded(let metadata, let reason), .playbackStarted):
            return .playing(metadata, stopReason: reason)
        case (.playing(let metadata, let reason), .playbackStopped):
            return .recorded(metadata, stopReason: reason)
        case (.playing, .playbackFailed(let message)):
            return .failed(.playbackFailed, message: message)
        default:
            throw RecordingError.illegalTransition
        }
    }
}
