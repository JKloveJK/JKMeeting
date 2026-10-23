@preconcurrency import AVFoundation
import Foundation

public enum OggOpusFileWriterError: Error, Equatable {
    case invalidConfiguration
    case invalidPCMFrame
    case encodingFailed
    case alreadyFinished
    case unreadableOutput
}

/// 把会话输出的 S16 PCM 交给 core 编码为 Ogg Opus 并流式写入文件。
/// 非线程安全；由 `RecordingPipeline` 在串行队列上调用。
public final class OggOpusFileWriter: ProcessedAudioFrameWriter {
    public static let bitrateBps = 24_000

    private let fileURL: URL
    private let sampleRate: Int
    private let channels: Int
    private let encoder: JKMeetingOggOpusEncoder
    private var handle: FileHandle?
    private var didFinish = false

    public init(fileURL: URL, sampleRate: Int, channels: Int) throws {
        self.fileURL = fileURL
        self.sampleRate = sampleRate
        self.channels = channels
        do {
            encoder = try JKMeetingOggOpusEncoder(
                sampleRate: sampleRate,
                channels: channels,
                bitrateBps: Self.bitrateBps,
                serial: UInt32.random(in: 1...UInt32.max)
            )
        } catch {
            throw OggOpusFileWriterError.invalidConfiguration
        }

        try FileManager.default.createDirectory(
            at: fileURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        guard FileManager.default.createFile(atPath: fileURL.path, contents: nil) else {
            throw CocoaError(.fileWriteUnknown)
        }
        do {
            let handle = try FileHandle(forWritingTo: fileURL)
            self.handle = handle
            try handle.write(contentsOf: encoder.headerData())
        } catch {
            discard()
            throw error
        }
    }

    public func append(_ frame: ProcessedAudioFrame) throws {
        guard !didFinish, let handle else { throw OggOpusFileWriterError.alreadyFinished }
        guard frame.sampleRate == sampleRate,
              frame.channels == channels,
              !frame.pcmData.isEmpty,
              frame.pcmData.count.isMultiple(of: MemoryLayout<Int16>.size * channels) else {
            throw OggOpusFileWriterError.invalidPCMFrame
        }
        let bytes: Data
        do {
            bytes = try encoder.encodePCMData(frame.pcmData)
        } catch {
            throw OggOpusFileWriterError.encodingFailed
        }
        if !bytes.isEmpty {
            try handle.write(contentsOf: bytes)
        }
    }

    public func finish() throws -> RecordingMetadata {
        guard !didFinish, let handle else { throw OggOpusFileWriterError.alreadyFinished }
        didFinish = true
        do {
            try handle.write(contentsOf: try encoder.finish())
            try handle.synchronize()
            try handle.close()
            self.handle = nil
        } catch {
            discard()
            throw OggOpusFileWriterError.unreadableOutput
        }

        let size: Int64
        do {
            let readable = try AVAudioFile(forReading: fileURL)
            let attributes = try FileManager.default.attributesOfItem(atPath: fileURL.path)
            size = (attributes[.size] as? NSNumber)?.int64Value ?? 0
            guard readable.length > 0, size > 0 else { throw OggOpusFileWriterError.unreadableOutput }
        } catch {
            discard()
            throw OggOpusFileWriterError.unreadableOutput
        }
        return RecordingMetadata(
            fileURL: fileURL,
            durationMs: Int(encoder.durationMs),
            sizeBytes: size,
            sampleRate: sampleRate,
            channels: channels
        )
    }

    public func discard() {
        try? handle?.close()
        handle = nil
        didFinish = true
        try? FileManager.default.removeItem(at: fileURL)
    }
}
