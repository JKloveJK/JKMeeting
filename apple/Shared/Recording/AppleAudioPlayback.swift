@preconcurrency import AVFoundation
import Foundation
import os

/// `.ogg` 走 core 解码 + `OggOpusFilePlayer`，其余格式（历史 m4a）仍由 `AVAudioPlayer` 播放。
@MainActor
public final class AppleAudioPlayback: NSObject, AudioPlayback, AVAudioPlayerDelegate {
    private static let logger = Logger(subsystem: "com.jkmeeting", category: "Playback")
    private let lock = NSLock()
    private var player: AVAudioPlayer?
    private var oggPlayer: OggOpusFilePlayer?
    public private(set) var lastPlaybackMetrics: PlaybackMetrics?

    public override init() {
        super.init()
    }

    public func play(
        fileURL: URL,
        completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void
    ) {
        stop()
        if fileURL.pathExtension.lowercased() == "ogg" {
            playOgg(fileURL: fileURL, completion: completion)
            return
        }
        do {
            let player = try AVAudioPlayer(contentsOf: fileURL)
            player.delegate = self
            player.prepareToPlay()
            guard player.play() else {
                completion(.failure(.playbackFailed))
                return
            }
            lock.lock()
            self.player = player
            lock.unlock()
            completion(.success(()))
        } catch {
            completion(.failure(.playbackFailed))
        }
    }

    private func playOgg(fileURL: URL, completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void) {
        let player = OggOpusFilePlayer()
        do {
            try player.play(
                fileURL: fileURL,
                onFirstFrame: { [weak self] metrics in
                    Task { @MainActor in self?.record(metrics, final: false) }
                },
                onFinish: { [weak self] result in
                    Task { @MainActor in
                        guard let self else { return }
                        if case .success(let metrics) = result { self.record(metrics, final: true) }
                        if self.oggPlayer === player { self.oggPlayer = nil }
                    }
                }
            )
            oggPlayer = player
            completion(.success(()))
        } catch {
            completion(.failure(.playbackFailed))
        }
    }

    private func record(_ metrics: PlaybackMetrics, final: Bool) {
        lastPlaybackMetrics = metrics
        Self.logger.info(
            "ogg playback \(final ? "finished" : "first_frame", privacy: .public) first_frame_ms=\(metrics.firstFrameLatencyMs, format: .fixed(precision: 1)) audible_ms=\(metrics.audibleFirstFrameLatencyMs, format: .fixed(precision: 1)) rendered=\(metrics.renderedFrames) decoded=\(metrics.decodedFrames) underruns=\(metrics.underrunCallbacks)"
        )
    }

    public func stop() {
        lock.lock()
        let player = self.player
        self.player = nil
        lock.unlock()
        player?.stop()
        oggPlayer?.stop()
        oggPlayer = nil
    }

    public func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        lock.lock()
        if self.player === player {
            self.player = nil
        }
        lock.unlock()
    }
}
