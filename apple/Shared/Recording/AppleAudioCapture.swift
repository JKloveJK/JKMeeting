@preconcurrency import AVFoundation
import Foundation

public final class AppleAudioCapture: NSObject, AudioCapture, @unchecked Sendable {
    /// `nil` 表示让 input node 使用当前硬件/Simulator 的原生输出格式。
    /// 不能在 installTap 时强制指定 16 kHz Int16，否则 AVFAudio 会以 NSException 终止进程。
    static let tapInstallationFormat: AVAudioFormat? = nil

    private let engine: AVAudioEngine
    private let stateLock = NSLock()
    private let lifecycle = AudioCaptureLifecycle()
    private let deliveryQueue = DispatchQueue(label: "com.jkmeeting.audio.capture.delivery")
    private let pendingFrameSlots: DispatchSemaphore
    private var onFrame: (@Sendable (CapturedPCMFrame) -> Void)?
    private var droppedFrames = 0

    public init(
        engine: AVAudioEngine = AVAudioEngine(),
        maximumPendingFrames: Int = 16
    ) {
        precondition(maximumPendingFrames > 0)
        self.engine = engine
        self.pendingFrameSlots = DispatchSemaphore(value: maximumPendingFrames)
        super.init()
    }

    public func requestPermission(completion: @escaping @Sendable (MicrophonePermission) -> Void) {
#if os(iOS)
        AVAudioApplication.requestRecordPermission { granted in
            DispatchQueue.main.async {
                completion(MicrophonePermission.map(isAuthorized: granted))
            }
        }
#else
        AVCaptureDevice.requestAccess(for: .audio) { granted in
            DispatchQueue.main.async {
                completion(MicrophonePermission.map(isAuthorized: granted))
            }
        }
#endif
    }

    public func start(
        onFrame: @escaping @Sendable (CapturedPCMFrame) -> Void,
        completion: @escaping @Sendable (Result<Void, AudioCaptureError>) -> Void
    ) {
        guard lifecycle.beginStart() else {
            completion(.failure(.engineStartFailed))
            return
        }
        let captureGeneration = lifecycle.generation
        stateLock.lock()
        self.onFrame = onFrame
        droppedFrames = 0
        stateLock.unlock()

        var tapInstalled = false
        do {
#if os(iOS)
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.record, mode: .measurement, options: [])
            try session.setActive(true)
#endif
            let inputNode = engine.inputNode
            let nativeFormat = inputNode.outputFormat(forBus: 0)
            let frameConverter: NativePCMFrameConverter
            do {
                frameConverter = try NativePCMFrameConverter(sourceFormat: nativeFormat)
            } catch {
                finishStart(with: .failure(.unsupportedInputFormat), completion: completion)
                return
            }

            inputNode.installTap(
                onBus: 0,
                bufferSize: 1_024,
                format: Self.tapInstallationFormat
            ) { [weak self] buffer, time in
                self?.forward(
                    buffer: buffer,
                    time: time,
                    converter: frameConverter,
                    generation: captureGeneration
                )
            }
            tapInstalled = true
            engine.prepare()
            try engine.start()
            lifecycle.markRunning()
            completion(.success(()))
        } catch {
            if tapInstalled {
                engine.inputNode.removeTap(onBus: 0)
            }
            finishStart(with: .failure(.engineStartFailed), completion: completion)
        }
    }

    public var droppedFrameCount: Int {
        stateLock.lock()
        defer { stateLock.unlock() }
        return droppedFrames
    }

    public func stop() {
        guard lifecycle.beginStop() else { return }
        engine.inputNode.removeTap(onBus: 0)
        engine.stop()
        deliveryQueue.sync {}
        stateLock.lock()
        onFrame = nil
        stateLock.unlock()
#if os(iOS)
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
#endif
        lifecycle.markStopped()
    }

    private func finishStart(
        with result: Result<Void, AudioCaptureError>,
        completion: @escaping @Sendable (Result<Void, AudioCaptureError>) -> Void
    ) {
        lifecycle.markStartFailed()
        stateLock.lock()
        onFrame = nil
        stateLock.unlock()
#if os(iOS)
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
#endif
        completion(result)
    }

    private func forward(
        buffer: AVAudioPCMBuffer,
        time: AVAudioTime,
        converter: NativePCMFrameConverter,
        generation: UInt64
    ) {
        guard pendingFrameSlots.wait(timeout: .now()) == .success else {
            stateLock.lock()
            droppedFrames += 1
            stateLock.unlock()
            return
        }

        let timestampMs: Int64
        let sampleTime: Int64?
        if time.isSampleTimeValid, buffer.format.sampleRate > 0 {
            timestampMs = Int64((Double(time.sampleTime) / buffer.format.sampleRate) * 1_000)
            sampleTime = time.sampleTime
        } else {
            timestampMs = 0
            sampleTime = nil
        }
        let hostTimeNs: Int64? = time.isHostTimeValid
            ? Int64(AVAudioTime.seconds(forHostTime: time.hostTime) * 1_000_000_000)
            : nil

        do {
            let frame = try converter.convert(
                buffer: buffer,
                timestampMs: timestampMs,
                sampleTime: sampleTime,
                hostTimeNs: hostTimeNs
            )
            stateLock.lock()
            let callback = onFrame
            stateLock.unlock()
            deliveryQueue.async { [weak self] in
                defer { self?.pendingFrameSlots.signal() }
                guard self?.lifecycle.canDeliver(generation: generation) == true else { return }
                callback?(frame)
            }
        } catch {
            pendingFrameSlots.signal()
            stateLock.lock()
            droppedFrames += 1
            stateLock.unlock()
        }
    }
}
