import AVFoundation
import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

/// 连接本地服务栈的 HLS 档位读取与切换测试。签名 URL 15 分钟过期，须由调用方现取并通过
/// `TEST_RUNNER_JK_HLS_URL` 注入（xcodebuild 会把 TEST_RUNNER_ 前缀变量去前缀后传给测试进程）。
@MainActor
final class HLSPlaybackControllerTests: XCTestCase {
    func testLiveHLSLoadsLadderAndSwitchesVariants() async throws {
        guard let raw = ProcessInfo.processInfo.environment["JK_HLS_URL"], let url = URL(string: raw) else {
            throw XCTSkip("未设置 TEST_RUNNER_JK_HLS_URL，跳过远程 HLS 测试")
        }
        let controller = HLSPlaybackController()
        controller.player.isMuted = true
        defer { controller.stop() }

        let variants = try await controller.load(url: url)
        XCTAssertEqual(variants.count, 3)
        XCTAssertEqual(variants.map(\.peakBitRate), variants.map(\.peakBitRate).sorted())
        controller.play()
        try await waitUntil(timeout: 10) { controller.indicatedBitrate != nil }

        var latencies = [String]()
        print("HLS_DIAG variants=\(variants.map(\.peakBitRate)) initial_indicated=\(controller.indicatedBitrate ?? -1)")
        for variant in [variants[0], variants[2], variants[1]] {
            controller.select(variant)
            do {
                try await waitUntil(timeout: 30) { controller.lastSwitchLatencyMs != nil }
            } catch {
                let events = controller.player.currentItem?.accessLog()?.events.map(\.indicatedBitrate) ?? []
                let buffered = controller.player.currentItem?.loadedTimeRanges.map { $0.timeRangeValue.end.seconds } ?? []
                XCTFail("切换到 \(variant.peakBitRate) 超时：access_log=\(events) buffered_end=\(buffered) time=\(controller.player.currentTime().seconds)")
                return
            }
            XCTAssertEqual(controller.indicatedBitrate, variant.peakBitRate)
            latencies.append("\(variant.kbps)kbps=\(Int(controller.lastSwitchLatencyMs ?? -1))ms")
        }
        print("HLS_SWITCH_METRICS variants=\(variants.map(\.kbps)) \(latencies.joined(separator: " "))")
    }

    func testSelectWithoutLoadedItemIsIgnored() {
        let controller = HLSPlaybackController()
        controller.select(HLSVariant(peakBitRate: 36_000, averageBitRate: nil))
        XCTAssertNil(controller.selectedVariant)
        XCTAssertNil(controller.lastSwitchLatencyMs)
        controller.play()
        XCTAssertFalse(controller.isPlaying)
    }

    private struct WaitTimeout: Error {}

    private func waitUntil(timeout: TimeInterval, _ condition: () -> Bool) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        while !condition() {
            guard Date() < deadline else { throw WaitTimeout() }
            try await Task.sleep(for: .milliseconds(50))
        }
    }
}
