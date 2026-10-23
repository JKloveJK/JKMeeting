@preconcurrency import AVFoundation
import Darwin
import Foundation
import Synchronization

public struct PlaybackMetrics: Sendable, Equatable {
    public let sampleRate: Int
    public let channels: Int
    /// 从调用 `play` 到渲染回调首次输出解码数据的主机时间差。
    public let firstFrameLatencyMs: Double
    /// 输出设备呈现延迟（`AVAudioOutputNode.presentationLatency`）。
    public let outputLatencyMs: Double
    public let decodedFrames: Int64
    public let renderedFrames: Int64
    /// 解码尚未结束、缓冲却不足一个渲染周期的回调次数。
    public let underrunCallbacks: Int

    public var audibleFirstFrameLatencyMs: Double { firstFrameLatencyMs + outputLatencyMs }
}

public enum OggOpusPlaybackError: Error, Equatable {
    case unreadableFile
    case invalidStream
    case engineFailed
}

/// 渲染线程与解码线程共享的状态；渲染路径只做原子操作和内存拷贝。
private final class RenderState: @unchecked Sendable {
    let ring: PCMRingBuffer
    let channels: Int
    let decodeFinished = Atomic<Bool>(false)
    let firstFrameHostTime = Atomic<UInt64>(0)
    let renderedFrames = Atomic<Int>(0)
    let underruns = Atomic<Int>(0)
    private let scratch: UnsafeMutablePointer<Float>
    private let scratchFrames = 8_192

    init(ring: PCMRingBuffer, channels: Int) {
        self.ring = ring
        self.channels = channels
        scratch = .allocate(capacity: scratchFrames * channels)
    }

    deinit {
        scratch.deallocate()
    }

    func render(frameCount: Int, timestamp: UnsafePointer<AudioTimeStamp>, buffers: UnsafeMutableAudioBufferListPointer) {
        let wanted = min(frameCount, scratchFrames)
        let frames = ring.read(into: scratch, count: wanted * channels) / channels
        for (channel, buffer) in buffers.enumerated() {
            guard let data = buffer.mData?.assumingMemoryBound(to: Float.self) else { continue }
            for frame in 0..<frames {
                data[frame] = scratch[frame * channels + min(channel, channels - 1)]
            }
            for frame in frames..<frameCount {
                data[frame] = 0
            }
        }
        guard frames > 0 else {
            if !decodeFinished.load(ordering: .acquiring) { underruns.add(1, ordering: .relaxed) }
            return
        }
        if firstFrameHostTime.load(ordering: .relaxed) == 0 {
            let hostTimeValid = timestamp.pointee.mFlags.contains(.hostTimeValid)
            firstFrameHostTime.store(hostTimeValid ? timestamp.pointee.mHostTime : mach_absolute_time(), ordering: .releasing)
        }
        if frames < frameCount && !decodeFinished.load(ordering: .acquiring) {
            underruns.add(1, ordering: .relaxed)
        }
        renderedFrames.add(frames, ordering: .releasing)
    }
}

/// 本地 Ogg Opus 文件播放器：壳层读文件，core 解码，`AVAudioSourceNode` 拉取 PCM。
/// 同步预缓冲约 100 ms 后启动引擎，其余部分在后台边解码边写入环形缓冲。
public final class OggOpusFilePlayer: @unchecked Sendable {
    public static let prebufferMs = 100
    static let readChunkBytes = 4_096
    static let bufferSeconds = 4

    private let lock = NSLock()
    private var engine: AVAudioEngine?
    private let cancelled = Atomic<Bool>(false)

    public init() {}

    /// 回调都在主队列执行；`stop()` 之后不再回调。
    public func play(
        fileURL: URL,
        onFirstFrame: @escaping @Sendable (PlaybackMetrics) -> Void = { _ in },
        onFinish: @escaping @Sendable (Result<PlaybackMetrics, OggOpusPlaybackError>) -> Void = { _ in }
    ) throws {
        let startHostTime = mach_absolute_time()
        let handle: FileHandle
        do {
            handle = try FileHandle(forReadingFrom: fileURL)
        } catch {
            throw OggOpusPlaybackError.unreadableFile
        }
        guard let decoder = JKMeetingOggOpusDecoder() else { throw OggOpusPlaybackError.invalidStream }

        var pending = [Float]()
        var decodedFrames: Int64 = 0
        var reachedEnd = false
        do {
            while true {
                guard let chunk = try handle.read(upToCount: Self.readChunkBytes), !chunk.isEmpty else {
                    reachedEnd = true
                    break
                }
                let pcm = try decoder.decode(chunk)
                Self.appendFloats(pcm, to: &pending)
                let rate = decoder.sampleRate
                if rate > 0 && decoder.outputFrames * 1_000 >= Int64(rate * Self.prebufferMs) { break }
            }
            if reachedEnd { try decoder.finish() }
        } catch {
            try? handle.close()
            throw OggOpusPlaybackError.invalidStream
        }
        decodedFrames = decoder.outputFrames
        let rate = decoder.sampleRate
        let channels = decoder.channels
        guard rate > 0, channels > 0, decodedFrames > 0,
              let format = AVAudioFormat(
                  commonFormat: .pcmFormatFloat32,
                  sampleRate: Double(rate),
                  channels: AVAudioChannelCount(channels),
                  interleaved: false
              ) else {
            try? handle.close()
            throw OggOpusPlaybackError.invalidStream
        }

        let state = RenderState(ring: PCMRingBuffer(capacity: rate * channels * Self.bufferSeconds), channels: channels)
        let written = pending.withUnsafeBufferPointer { state.ring.write(Self.wholeFrames($0, channels: channels, limit: state.ring.availableToWrite)) }
        pending.removeFirst(written)
        if reachedEnd && pending.isEmpty { state.decodeFinished.store(true, ordering: .releasing) }

        let engine = AVAudioEngine()
        let node = AVAudioSourceNode(format: format) { _, timestamp, frameCount, bufferList in
            state.render(
                frameCount: Int(frameCount),
                timestamp: timestamp,
                buffers: UnsafeMutableAudioBufferListPointer(bufferList)
            )
            return noErr
        }
        engine.attach(node)
        engine.connect(node, to: engine.mainMixerNode, format: format)
        do {
            try engine.start()
        } catch {
            try? handle.close()
            throw OggOpusPlaybackError.engineFailed
        }
        let outputLatencyMs = engine.outputNode.presentationLatency * 1_000
        lock.withLock { self.engine = engine }

        let context = DecodeContext(
            handle: handle, decoder: decoder, state: state, pending: pending, reachedEnd: reachedEnd,
            startHostTime: startHostTime, outputLatencyMs: outputLatencyMs, sampleRate: rate, channels: channels
        )
        Thread.detachNewThread { [self] in
            self.continueDecoding(context, onFirstFrame: onFirstFrame, onFinish: onFinish)
        }
    }

    public func stop() {
        cancelled.store(true, ordering: .releasing)
        let engine = lock.withLock { () -> AVAudioEngine? in
            defer { self.engine = nil }
            return self.engine
        }
        engine?.stop()
    }

    private struct DecodeContext: @unchecked Sendable {
        let handle: FileHandle
        let decoder: JKMeetingOggOpusDecoder
        let state: RenderState
        var pending: [Float]
        var reachedEnd: Bool
        let startHostTime: UInt64
        let outputLatencyMs: Double
        let sampleRate: Int
        let channels: Int
    }

    private func continueDecoding(
        _ initial: DecodeContext,
        onFirstFrame: @escaping @Sendable (PlaybackMetrics) -> Void,
        onFinish: @escaping @Sendable (Result<PlaybackMetrics, OggOpusPlaybackError>) -> Void
    ) {
        var context = initial
        var firstFrameReported = false
        defer { try? context.handle.close() }
        let reportFirstFrame = { [self] in
            guard !firstFrameReported, context.state.firstFrameHostTime.load(ordering: .acquiring) != 0 else { return }
            firstFrameReported = true
            let metrics = self.metrics(context)
            DispatchQueue.main.async { if !self.cancelled.load(ordering: .acquiring) { onFirstFrame(metrics) } }
        }
        let fail = { [self] (error: OggOpusPlaybackError) in
            self.stop()
            DispatchQueue.main.async { onFinish(.failure(error)) }
        }

        while !cancelled.load(ordering: .acquiring) {
            reportFirstFrame()
            if !context.pending.isEmpty {
                let space = context.state.ring.availableToWrite
                let written = context.pending.withUnsafeBufferPointer {
                    context.state.ring.write(Self.wholeFrames($0, channels: context.channels, limit: space))
                }
                context.pending.removeFirst(written)
                if written == 0 { Thread.sleep(forTimeInterval: 0.005) }
                continue
            }
            if context.reachedEnd {
                context.state.decodeFinished.store(true, ordering: .releasing)
                break
            }
            do {
                guard let chunk = try context.handle.read(upToCount: Self.readChunkBytes), !chunk.isEmpty else {
                    try context.decoder.finish()
                    context.reachedEnd = true
                    continue
                }
                Self.appendFloats(try context.decoder.decode(chunk), to: &context.pending)
            } catch {
                fail(.invalidStream)
                return
            }
        }

        let total = Int(context.decoder.outputFrames)
        while !cancelled.load(ordering: .acquiring) && context.state.renderedFrames.load(ordering: .acquiring) < total {
            reportFirstFrame()
            Thread.sleep(forTimeInterval: 0.01)
        }
        guard !cancelled.load(ordering: .acquiring) else { return }
        reportFirstFrame()
        let metrics = metrics(context)
        stop()
        DispatchQueue.main.async { onFinish(.success(metrics)) }
    }

    private func metrics(_ context: DecodeContext) -> PlaybackMetrics {
        let firstHostTime = context.state.firstFrameHostTime.load(ordering: .acquiring)
        let latency = firstHostTime > context.startHostTime ? Self.milliseconds(firstHostTime - context.startHostTime) : 0
        return PlaybackMetrics(
            sampleRate: context.sampleRate,
            channels: context.channels,
            firstFrameLatencyMs: latency,
            outputLatencyMs: context.outputLatencyMs,
            decodedFrames: context.decoder.outputFrames,
            renderedFrames: Int64(context.state.renderedFrames.load(ordering: .acquiring)),
            underrunCallbacks: context.state.underruns.load(ordering: .relaxed)
        )
    }

    private static func appendFloats(_ pcm: Data, to output: inout [Float]) {
        pcm.withUnsafeBytes { raw in
            for sample in raw.bindMemory(to: Int16.self) {
                output.append(Float(sample) / 32_768)
            }
        }
    }

    private static func wholeFrames(_ samples: UnsafeBufferPointer<Float>, channels: Int, limit: Int) -> UnsafeBufferPointer<Float> {
        let count = min(samples.count, limit) / channels * channels
        return UnsafeBufferPointer(rebasing: samples[0..<count])
    }

    private static func milliseconds(_ hostTicks: UInt64) -> Double {
        var timebase = mach_timebase_info_data_t()
        mach_timebase_info(&timebase)
        return Double(hostTicks) * Double(timebase.numer) / Double(timebase.denom) / 1_000_000
    }
}
