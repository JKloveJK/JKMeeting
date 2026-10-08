@preconcurrency import AVFoundation
import Foundation

@MainActor
public final class AppleAudioPlayback: NSObject, AudioPlayback, AVAudioPlayerDelegate {
    private let lock = NSLock()
    private var player: AVAudioPlayer?

    public override init() {
        super.init()
    }

    public func play(
        fileURL: URL,
        completion: @escaping @Sendable (Result<Void, RecordingError>) -> Void
    ) {
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

    public func stop() {
        lock.lock()
        let player = self.player
        self.player = nil
        lock.unlock()
        player?.stop()
    }

    public func audioPlayerDidFinishPlaying(_ player: AVAudioPlayer, successfully flag: Bool) {
        lock.lock()
        if self.player === player {
            self.player = nil
        }
        lock.unlock()
    }
}
