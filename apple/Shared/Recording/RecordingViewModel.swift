import Combine
import Foundation

@MainActor
public protocol RecordingManaging: AnyObject {
    func start(completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void)
    func stop(completion: @escaping @Sendable (Result<RecordingMetadata, RecordingError>) -> Void)
    func play(fileURL: URL, completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void)
    func stopPlayback()
    func setAutomaticStopHandler(_ handler: @escaping @MainActor (Result<RecordingMetadata, RecordingError>, RecordingStopReason) -> Void)
}

public extension RecordingManaging {
    func setAutomaticStopHandler(_ handler: @escaping @MainActor (Result<RecordingMetadata, RecordingError>, RecordingStopReason) -> Void) {}
}

@MainActor
public final class RecordingViewModel: ObservableObject {
    @Published public private(set) var state: RecordingState = .idle
    @Published public private(set) var transcriptionPhase: TranscriptionPhase = .unavailable
    @Published public private(set) var transcriptionText: String?
    @Published public private(set) var remotePlayback: RemotePlayback?
    public let hlsPlayback: HLSPlaybackController
    private let manager: RecordingManaging
    private let transcriptionClient: RecordingTranscribing?
    private let remotePlaybackProvider: RemotePlaybackProviding?
    private var transcriptionTask: Task<Void, Never>?
    private var remotePlaybackTask: Task<Void, Never>?
    private let remotePlaybackPollInterval: Duration
    private var recordingGeneration = 0
    private var latestMetadata: RecordingMetadata?
    private var latestStopReason: RecordingStopReason = .user

    public init(
        manager: RecordingManaging,
        transcriptionClient: RecordingTranscribing? = try? RecordingAPIClient(),
        hlsPlayback: HLSPlaybackController = HLSPlaybackController(),
        remotePlaybackPollInterval: Duration = .seconds(1)
    ) {
        self.manager = manager
        self.remotePlaybackPollInterval = remotePlaybackPollInterval
        self.transcriptionClient = transcriptionClient
        self.remotePlaybackProvider = transcriptionClient as? RemotePlaybackProviding
        self.hlsPlayback = hlsPlayback
        self.transcriptionPhase = transcriptionClient == nil ? .unavailable : .ready
        manager.setAutomaticStopHandler { [weak self] result, reason in
            guard let self, case .recording = self.state else { return }
            self.applyStop(result, reason: reason)
        }
    }

    public func startRecording() {
        guard state != .recording && state != .stopping && state != .requestingPermission && state != .startingCapture else { return }
        recordingGeneration += 1
        transcriptionTask?.cancel()
        transcriptionTask = nil
        transcriptionPhase = transcriptionClient == nil ? .unavailable : .ready
        transcriptionText = nil
        latestMetadata = nil
        resetRemotePlayback()
        manager.stopPlayback()
        state = .requestingPermission
        manager.start { [weak self] result in
            Task { @MainActor in
                guard let self else { return }
                switch result {
                case .success:
                    self.state = .recording
                case .failure(let error):
                    self.state = .failed(error, message: error == .permissionDenied ? "请在系统设置中允许 JKMeeting 使用麦克风" : "无法开始录音")
                }
            }
        }
    }

    public func stopRecording() {
        guard case .recording = state else { return }
        state = .stopping
        manager.stop { [weak self] result in
            Task { @MainActor in
                guard let self else { return }
                self.applyStop(result, reason: .user)
            }
        }
    }

    private func applyStop(_ result: Result<RecordingMetadata, RecordingError>, reason: RecordingStopReason) {
        switch result {
        case .success(let metadata):
            latestMetadata = metadata
            latestStopReason = reason
            state = .recorded(metadata, stopReason: reason)
        case .failure(let error):
            state = .failed(error, message: "录音文件无效")
        }
    }

    public func startTranscription() {
        guard let metadata = latestMetadata, let transcriptionClient else {
            transcriptionPhase = .unavailable
            return
        }
        guard transcriptionTask == nil else { return }
        let generation = recordingGeneration
        transcriptionPhase = .uploading
        transcriptionTask = Task { [weak self] in
            do {
                let text = try await transcriptionClient.transcribe(metadata) { [weak self] phase in
                    guard self?.recordingGeneration == generation else { return }
                    self?.transcriptionPhase = phase
                }
                guard let self, recordingGeneration == generation, !Task.isCancelled else { return }
                transcriptionText = text
                transcriptionPhase = .done
                transcriptionTask = nil
                refreshRemotePlayback()
            } catch is CancellationError {
                guard let self, recordingGeneration == generation else { return }
                transcriptionPhase = .ready
                transcriptionTask = nil
            } catch {
                guard let self, recordingGeneration == generation else { return }
                transcriptionPhase = .failed((error as? LocalizedError)?.errorDescription ?? "网络错误，请检查服务后重试")
                transcriptionTask = nil
            }
        }
    }

    public func cancelTranscription() {
        recordingGeneration += 1
        transcriptionTask?.cancel()
        transcriptionTask = nil
        if transcriptionClient != nil { transcriptionPhase = .ready }
    }

    public func playRecording() {
        guard let metadata = latestMetadata else { return }
        hlsPlayback.stop()
        manager.play(fileURL: metadata.fileURL) { [weak self] result in
            Task { @MainActor in
                guard let self else { return }
                switch result {
                case .success:
                    self.state = .playing(metadata, stopReason: self.latestStopReason)
                case .failure(let error):
                    self.state = .failed(error, message: "无法播放录音")
                }
            }
        }
    }

    public func stopPlayback() {
        manager.stopPlayback()
        guard let metadata = latestMetadata else { return }
        state = .recorded(metadata, stopReason: latestStopReason)
    }

    /// 转码与转写并行执行，转写完成时 HLS 通常已就绪；未就绪时短暂轮询，失败或超时只隐藏远程回放，不影响本地回放。
    public func refreshRemotePlayback(maxAttempts: Int = 30) {
        guard let provider = remotePlaybackProvider, let recordingID = provider.lastRecordingID else { return }
        let generation = recordingGeneration
        let pollInterval = remotePlaybackPollInterval
        remotePlaybackTask?.cancel()
        remotePlaybackTask = Task { [weak self] in
            for _ in 0..<maxAttempts {
                guard let playback = try? await provider.remotePlayback(recordingID: recordingID) else { break }
                guard let self, self.recordingGeneration == generation, !Task.isCancelled else { return }
                self.remotePlayback = playback
                if playback.isHLSReady || ["failed", "none"].contains(playback.transcodeStatus) { return }
                try? await Task.sleep(for: pollInterval)
            }
        }
    }

    public func playRemote() async {
        guard let url = remotePlayback?.hlsURL else { return }
        manager.stopPlayback()
        do {
            try await hlsPlayback.load(url: url)
            hlsPlayback.play()
        } catch {
            hlsPlayback.stop()
        }
    }

    public func stopRemote() {
        hlsPlayback.stop()
    }

    private func resetRemotePlayback() {
        remotePlaybackTask?.cancel()
        remotePlaybackTask = nil
        remotePlayback = nil
        hlsPlayback.stop()
    }
}
