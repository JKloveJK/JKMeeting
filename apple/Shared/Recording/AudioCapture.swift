import Foundation

public enum MicrophonePermission: Equatable, Sendable {
    case unknown
    case granted
    case denied

    public static func map(isAuthorized: Bool) -> MicrophonePermission {
        isAuthorized ? .granted : .denied
    }
}

public struct CapturedPCMFrame: Equatable, Sendable {
    public let pcmData: Data
    public let sampleRate: Int
    public let channels: Int
    public let timestampMs: Int64

    public init(pcmData: Data, sampleRate: Int, channels: Int, timestampMs: Int64) {
        self.pcmData = pcmData
        self.sampleRate = sampleRate
        self.channels = channels
        self.timestampMs = timestampMs
    }
}

public enum AudioCaptureError: Error, Equatable, Sendable {
    case permissionDenied
    case engineStartFailed
    case unsupportedInputFormat
    case interrupted
    case durationLimitReached
}

public protocol AudioCapture: AnyObject {
    func requestPermission(completion: @escaping @Sendable (MicrophonePermission) -> Void)
    func start(
        onFrame: @escaping @Sendable (CapturedPCMFrame) -> Void,
        completion: @escaping @Sendable (Result<Void, AudioCaptureError>) -> Void
    )
    func stop()
}

@MainActor
public protocol AudioPlayback: AnyObject {
    func play(fileURL: URL, completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void)
    func stop()
}
