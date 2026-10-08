@preconcurrency import AVFoundation
import Foundation

private final class PipelineStore: @unchecked Sendable {
    private let queue = DispatchQueue(label: "com.jkmeeting.audio.pipeline")
    private var pipeline: RecordingPipeline?
    private var processingError: RecordingError?

    func install(_ pipeline: RecordingPipeline) {
        queue.sync {
            self.pipeline = pipeline
            processingError = nil
        }
    }

    func enqueue(_ frame: CapturedPCMFrame) {
        queue.async {
            do {
                try self.pipeline?.process(frame)
            } catch {
                self.processingError = .captureFailed
            }
        }
    }

    func finish() -> Result<RecordingMetadata, RecordingError> {
        queue.sync {
            defer { pipeline = nil }
            guard processingError == nil, let pipeline else {
                pipeline?.discard()
                return .failure(.captureFailed)
            }
            do {
                return .success(try pipeline.finish())
            } catch {
                return .failure(.invalidRecordedFile)
            }
        }
    }
}

@MainActor
public final class AppleRecordingManager: RecordingManaging {
    private let capture: AudioCapture
    private let playback: AudioPlayback
    private let pipelineStore = PipelineStore()
    private let recordingsDirectory: URL
    private let durationLimit: Duration
    private var durationTask: Task<Void, Never>?
    private var interruptionObservers = [NSObjectProtocol]()
    private var automaticStopHandler: (@MainActor (Result<RecordingMetadata, RecordingError>, RecordingStopReason) -> Void)?
    private var isCapturing = false

    public init(
        capture: AudioCapture = AppleAudioCapture(),
        playback: AudioPlayback = AppleAudioPlayback(),
        recordingsDirectory: URL? = nil,
        durationLimit: Duration = .seconds(300)
    ) {
        self.capture = capture
        self.playback = playback
        self.durationLimit = durationLimit
        self.recordingsDirectory = recordingsDirectory ?? FileManager.default.urls(
            for: .applicationSupportDirectory,
            in: .userDomainMask
        )[0].appendingPathComponent("JKMeeting/Recordings", isDirectory: true)
    }

    public func start(completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) {
        capture.requestPermission { [weak self] permission in
            Task { @MainActor in
                guard let self else { return }
                guard permission == .granted else {
                    completion(.failure(.permissionDenied))
                    return
                }
                self.startCapture(completion: completion)
            }
        }
    }

    public func stop(completion: @escaping @Sendable (Result<RecordingMetadata, RecordingError>) -> Void) {
        guard isCapturing else { completion(.failure(.captureFailed)); return }
        completion(finishCapture())
    }

    public func setAutomaticStopHandler(_ handler: @escaping @MainActor (Result<RecordingMetadata, RecordingError>, RecordingStopReason) -> Void) {
        automaticStopHandler = handler
    }

    private func finishCapture() -> Result<RecordingMetadata, RecordingError> {
        isCapturing = false
        durationTask?.cancel()
        durationTask = nil
        for observer in interruptionObservers { NotificationCenter.default.removeObserver(observer) }
        interruptionObservers.removeAll()
        capture.stop()
        return pipelineStore.finish()
    }

    private func automaticallyStop(reason: RecordingStopReason) {
        guard isCapturing else { return }
        let result = finishCapture()
        automaticStopHandler?(result, reason)
    }

    private func armAutomaticStop() {
        durationTask = Task { [weak self, durationLimit] in
            do { try await Task.sleep(for: durationLimit) }
            catch { return }
            self?.automaticallyStop(reason: .durationLimit)
        }
#if os(iOS)
        let interruption = NotificationCenter.default.addObserver(
            forName: AVAudioSession.interruptionNotification, object: AVAudioSession.sharedInstance(), queue: .main
        ) { [weak self] note in
            let isBegan = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt) == AVAudioSession.InterruptionType.began.rawValue
            guard isBegan else { return }
            Task { @MainActor [weak self] in self?.automaticallyStop(reason: .interrupted) }
        }
        interruptionObservers.append(interruption)
#endif
        let configuration = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor [weak self] in self?.automaticallyStop(reason: .interrupted) }
        }
        interruptionObservers.append(configuration)
    }

    public func play(fileURL: URL, completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) {
        playback.play(fileURL: fileURL, completion: completion)
    }

    public func stopPlayback() {
        playback.stop()
    }

    private func startCapture(completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) {
        do {
            try FileManager.default.createDirectory(at: recordingsDirectory, withIntermediateDirectories: true)
            let fileURL = recordingsDirectory.appendingPathComponent(UUID().uuidString).appendingPathExtension("m4a")
            let writer = try AppleAudioFileWriter(fileURL: fileURL, sampleRate: 16_000, channels: 1)
            pipelineStore.install(RecordingPipeline(writer: writer))
            capture.start(onFrame: { [pipelineStore] frame in
                pipelineStore.enqueue(frame)
            }, completion: { result in
                Task { @MainActor in
                    switch result {
                    case .success:
                        self.isCapturing = true
                        self.armAutomaticStop()
                        completion(.success(()))
                    case .failure:
                        _ = self.pipelineStore.finish()
                        completion(.failure(.captureFailed))
                    }
                }
            })
        } catch {
            completion(.failure(.invalidRecordedFile))
        }
    }
}
