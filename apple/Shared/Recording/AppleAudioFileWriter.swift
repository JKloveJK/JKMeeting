@preconcurrency import AVFoundation
import Foundation

public enum AppleAudioFileWriterError: Error, Equatable {
    case invalidPCMFrame
    case alreadyFinished
    case unreadableOutput
}

public final class AppleAudioFileWriter: ProcessedAudioFrameWriter {
    private let fileURL: URL
    private let sampleRate: Int
    private let channels: Int
    private let pcmFormat: AVAudioFormat
    private var audioFile: AVAudioFile?
    private var didFinish = false

    public init(fileURL: URL, sampleRate: Int, channels: Int) throws {
        self.fileURL = fileURL
        self.sampleRate = sampleRate
        self.channels = channels
        guard let format = AVAudioFormat(
            commonFormat: .pcmFormatInt16,
            sampleRate: Double(sampleRate),
            channels: AVAudioChannelCount(channels),
            interleaved: true
        ) else {
            throw AppleAudioFileWriterError.invalidPCMFrame
        }
        self.pcmFormat = format

        try FileManager.default.createDirectory(
            at: fileURL.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: sampleRate,
            AVNumberOfChannelsKey: channels,
            AVEncoderBitRateKey: 32_000
        ]
        self.audioFile = try AVAudioFile(
            forWriting: fileURL,
            settings: settings,
            commonFormat: .pcmFormatInt16,
            interleaved: true
        )
    }

    public func append(_ frame: ProcessedAudioFrame) throws {
        guard !didFinish,
              frame.sampleRate == sampleRate,
              frame.channels == channels,
              frame.pcmData.count.isMultiple(of: MemoryLayout<Int16>.size * channels) else {
            throw AppleAudioFileWriterError.invalidPCMFrame
        }
        guard let audioFile else {
            throw AppleAudioFileWriterError.alreadyFinished
        }

        let frameCount = frame.pcmData.count / (MemoryLayout<Int16>.size * channels)
        guard let buffer = AVAudioPCMBuffer(
            pcmFormat: pcmFormat,
            frameCapacity: AVAudioFrameCount(frameCount)
        ), let destination = buffer.int16ChannelData else {
            throw AppleAudioFileWriterError.invalidPCMFrame
        }

        buffer.frameLength = AVAudioFrameCount(frameCount)
        frame.pcmData.withUnsafeBytes { source in
            guard let baseAddress = source.baseAddress else { return }
            destination[0].update(from: baseAddress.assumingMemoryBound(to: Int16.self), count: frameCount * channels)
        }
        try audioFile.write(from: buffer)
    }

    public func finish() throws -> RecordingMetadata {
        guard !didFinish else { throw AppleAudioFileWriterError.alreadyFinished }
        didFinish = true
        audioFile = nil

        let readableFile: AVAudioFile
        do {
            readableFile = try AVAudioFile(forReading: fileURL)
        } catch {
            discard()
            throw AppleAudioFileWriterError.unreadableOutput
        }
        let attributes = try FileManager.default.attributesOfItem(atPath: fileURL.path)
        let size = (attributes[.size] as? NSNumber)?.int64Value ?? 0
        guard size > 0 else {
            discard()
            throw AppleAudioFileWriterError.unreadableOutput
        }
        let durationMs = Int((Double(readableFile.length) / readableFile.processingFormat.sampleRate) * 1_000)
        return RecordingMetadata(
            fileURL: fileURL,
            durationMs: durationMs,
            sizeBytes: size,
            sampleRate: sampleRate,
            channels: channels
        )
    }

    public func discard() {
        audioFile = nil
        didFinish = true
        try? FileManager.default.removeItem(at: fileURL)
    }
}
