@preconcurrency import AVFoundation
import Combine
import Foundation
import os

public struct HLSVariant: Hashable, Sendable {
    public let peakBitRate: Double
    public let averageBitRate: Double?

    public var kbps: Int { Int((averageBitRate ?? peakBitRate) / 1000) }
}

/// 远程 HLS 回放：读取 master playlist 档位，通过 `preferredPeakBitRate` 限定档位，
/// 并以 access log 的 `indicatedBitrate`（即 master 中的 BANDWIDTH）确认切换生效、测量切换耗时。
@MainActor
public final class HLSPlaybackController: ObservableObject {
    private static let logger = Logger(subsystem: "com.jkmeeting", category: "HLSPlayback")

    @Published public private(set) var variants: [HLSVariant] = []
    @Published public private(set) var selectedVariant: HLSVariant?
    @Published public private(set) var indicatedBitrate: Double?
    @Published public private(set) var lastSwitchLatencyMs: Double?
    @Published public private(set) var isPlaying = false

    public let player: AVPlayer
    private var item: AVPlayerItem?
    private var observers: [NSObjectProtocol] = []
    private var pendingSwitch: (target: Double, startedAt: ContinuousClock.Instant)?

    public init(player: AVPlayer = AVPlayer()) {
        self.player = player
    }

    @discardableResult
    public func load(url: URL) async throws -> [HLSVariant] {
        stop()
        let asset = AVURLAsset(url: url)
        let loaded = try await asset.load(.variants)
        variants = loaded
            .compactMap { variant in variant.peakBitRate.map { HLSVariant(peakBitRate: $0, averageBitRate: variant.averageBitRate) } }
            .sorted { $0.peakBitRate < $1.peakBitRate }
        let item = AVPlayerItem(asset: asset)
        observe(item)
        self.item = item
        player.replaceCurrentItem(with: item)
        return variants
    }

    public func play() {
        guard item != nil else { return }
        player.play()
        isPlaying = true
    }

    public func stop() {
        player.pause()
        player.replaceCurrentItem(with: nil)
        observers.forEach(NotificationCenter.default.removeObserver)
        observers.removeAll()
        item = nil
        isPlaying = false
        selectedVariant = nil
        indicatedBitrate = nil
        pendingSwitch = nil
    }

    /// `nil` 表示自动选择档位；否则播放器只会选择 BANDWIDTH 不超过目标档位的 variant。
    public func select(_ variant: HLSVariant?) {
        guard let item else { return }
        selectedVariant = variant
        item.preferredPeakBitRate = variant?.peakBitRate ?? 0
        lastSwitchLatencyMs = nil
        pendingSwitch = variant.map { ($0.peakBitRate, .now) }
        if let variant, indicatedBitrate == variant.peakBitRate {
            lastSwitchLatencyMs = 0
            pendingSwitch = nil
        }
    }

    private func observe(_ item: AVPlayerItem) {
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: AVPlayerItem.newAccessLogEntryNotification, object: item, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.accessLogUpdated() }
        })
        observers.append(center.addObserver(forName: AVPlayerItem.didPlayToEndTimeNotification, object: item, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.isPlaying = false }
        })
    }

    private func accessLogUpdated() {
        guard let event = item?.accessLog()?.events.last, event.indicatedBitrate > 0 else { return }
        indicatedBitrate = event.indicatedBitrate
        guard let pending = pendingSwitch, event.indicatedBitrate == pending.target else { return }
        let elapsed = ContinuousClock.now - pending.startedAt
        let milliseconds = Double(elapsed.components.seconds) * 1000 + Double(elapsed.components.attoseconds) / 1e15
        lastSwitchLatencyMs = milliseconds
        pendingSwitch = nil
        Self.logger.info("HLS 档位切换生效 indicated_bitrate=\(event.indicatedBitrate, privacy: .public) switch_ms=\(milliseconds, privacy: .public)")
    }
}
