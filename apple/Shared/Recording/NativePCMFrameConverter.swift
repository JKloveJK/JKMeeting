@preconcurrency import AVFoundation
import Foundation

public enum NativePCMFrameConverterError: Error, Equatable {
    case invalidSourceFormat
    case converterUnavailable
    case conversionFailed
    case emptyOutput
}

/// 将 AVAudioEngine 的硬件原生 PCM 转换为 C++ core 接受的交错 S16LE 单声道数据。
public final class NativePCMFrameConverter: @unchecked Sendable {
    private let converter: AVAudioConverter
    private let destinationFormat: AVAudioFormat
    private let lock = NSLock()

    public init(sourceFormat: AVAudioFormat) throws {
        guard sourceFormat.sampleRate > 0, sourceFormat.channelCount > 0 else {
            throw NativePCMFrameConverterError.invalidSourceFormat
        }
        guard let destinationFormat = AVAudioFormat(
            commonFormat: .pcmFormatInt16,
            sampleRate: sourceFormat.sampleRate,
            channels: 1,
            interleaved: true
        ) else {
            throw NativePCMFrameConverterError.invalidSourceFormat
        }
        guard let converter = AVAudioConverter(from: sourceFormat, to: destinationFormat) else {
            throw NativePCMFrameConverterError.converterUnavailable
        }
        self.converter = converter
        self.destinationFormat = destinationFormat
    }

    public func convert(buffer: AVAudioPCMBuffer, timestampMs: Int64) throws -> CapturedPCMFrame {
        lock.lock()
        defer { lock.unlock() }

        let capacity = max(1, AVAudioFrameCount(ceil(
            Double(buffer.frameLength) * destinationFormat.sampleRate / buffer.format.sampleRate
        )))
        guard let output = AVAudioPCMBuffer(pcmFormat: destinationFormat, frameCapacity: capacity) else {
            throw NativePCMFrameConverterError.conversionFailed
        }

        var suppliedInput = false
        var conversionError: NSError?
        let status = converter.convert(to: output, error: &conversionError) { _, inputStatus in
            if suppliedInput {
                inputStatus.pointee = .noDataNow
                return nil
            }
            suppliedInput = true
            inputStatus.pointee = .haveData
            return buffer
        }

        guard conversionError == nil,
              status == .haveData || status == .inputRanDry,
              output.frameLength > 0 else {
            throw NativePCMFrameConverterError.conversionFailed
        }

        let audioBuffer = output.audioBufferList.pointee.mBuffers
        guard let bytes = audioBuffer.mData, audioBuffer.mDataByteSize > 0 else {
            throw NativePCMFrameConverterError.emptyOutput
        }

        return CapturedPCMFrame(
            pcmData: Data(bytes: bytes, count: Int(audioBuffer.mDataByteSize)),
            sampleRate: Int(destinationFormat.sampleRate),
            channels: Int(destinationFormat.channelCount),
            timestampMs: timestampMs
        )
    }
}
