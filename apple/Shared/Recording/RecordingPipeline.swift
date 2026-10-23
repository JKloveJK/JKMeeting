import Foundation
import os

public protocol ProcessedAudioFrameWriter: AnyObject {
    func append(_ frame: ProcessedAudioFrame) throws
    func finish() throws -> RecordingMetadata
    func discard()
}

public typealias AudioSessionFactory = (
    _ inputSampleRate: Int,
    _ channels: Int,
    _ outputSampleRate: Int
) throws -> any AudioSessionProcessing

public final class RecordingPipeline {
    private static let logger = Logger(subsystem: "com.jkmeeting", category: "RecordingPipeline")

    private let makeSession: AudioSessionFactory
    private let writer: any ProcessedAudioFrameWriter
    private let targetSampleRate: Int
    private var session: (any AudioSessionProcessing)?
    public private(set) var coreProcessingNanoseconds: UInt64 = 0
    public private(set) var lastSessionStats: AudioSessionStats?

    public var coreProcessingDurationMs: Double {
        Double(coreProcessingNanoseconds) / 1_000_000.0
    }

    public init(
        writer: any ProcessedAudioFrameWriter,
        targetSampleRate: Int = 16_000,
        makeSession: @escaping AudioSessionFactory = { inputRate, channels, outputRate in
            try CoreAudioSession(inputSampleRate: inputRate, channels: channels, outputSampleRate: outputRate)
        }
    ) {
        self.writer = writer
        self.targetSampleRate = targetSampleRate
        self.makeSession = makeSession
    }

    public func process(_ frame: CapturedPCMFrame) throws {
        do {
            // 计时包含写入器中的编码（Opus 在 core 内编码），与 B3 的“会话加编码”口径一致。
            try measure {
                let processed = try currentSession(for: frame).process(frame)
                if !processed.pcmData.isEmpty {
                    try writer.append(processed)
                }
            }
        } catch {
            writer.discard()
            throw error
        }
    }

    public func finish() throws -> RecordingMetadata {
        if let session {
            do {
                try measure {
                    let tail = try session.finish()
                    if !tail.pcmData.isEmpty {
                        try writer.append(tail)
                    }
                }
            } catch {
                writer.discard()
                throw error
            }
            let stats = session.stats
            lastSessionStats = stats
            Self.logger.info("""
            audio_session input_frames=\(stats.inputFrames) output_frames=\(stats.outputFrames) \
            gap_events=\(stats.gapEvents) filled_gap_frames=\(stats.filledGapFrames) \
            unfilled_gap_frames=\(stats.unfilledGapFrames) overlap_discarded=\(stats.overlapFramesDiscarded) \
            jitter_ms=\(stats.jitterMs) max_transit_delta_ms=\(stats.maxTransitDeltaMs) \
            speech_ms=\(stats.speechMs) silence_ms=\(stats.silenceMs) core_ms=\(self.coreProcessingDurationMs)
            """)
        }
        return try writer.finish()
    }

    public func discard() {
        writer.discard()
    }

    private func currentSession(for frame: CapturedPCMFrame) throws -> any AudioSessionProcessing {
        if let session {
            return session
        }
        let created = try makeSession(frame.sampleRate, frame.channels, targetSampleRate)
        session = created
        return created
    }

    private func measure<T>(_ body: () throws -> T) rethrows -> T {
        let startNs = DispatchTime.now().uptimeNanoseconds
        defer {
            let endNs = DispatchTime.now().uptimeNanoseconds
            if endNs >= startNs {
                coreProcessingNanoseconds += endNs - startNs
            }
        }
        return try body()
    }
}
