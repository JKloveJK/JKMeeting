import Foundation

final class AudioCaptureLifecycle: @unchecked Sendable {
    private enum State {
        case idle
        case starting
        case running
        case stopping
    }

    private let lock = NSLock()
    private var state: State = .idle
    private var currentGeneration: UInt64 = 0

    func beginStart() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard state == .idle else { return false }
        currentGeneration &+= 1
        state = .starting
        return true
    }

    func markRunning() {
        lock.lock()
        defer { lock.unlock() }
        if state == .starting {
            state = .running
        }
    }

    func markStartFailed() {
        lock.lock()
        defer { lock.unlock() }
        if state == .starting {
            state = .idle
        }
    }

    func beginStop() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard state == .running else { return false }
        state = .stopping
        return true
    }

    func markStopped() {
        lock.lock()
        defer { lock.unlock() }
        state = .idle
    }

    var generation: UInt64 {
        lock.lock()
        defer { lock.unlock() }
        return currentGeneration
    }

    func isRunning(generation: UInt64) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return state == .running && currentGeneration == generation
    }

    func canDeliver(generation: UInt64) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return (state == .running || state == .stopping) && currentGeneration == generation
    }
}
