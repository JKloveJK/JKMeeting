import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

@MainActor
final class AppleAudioPlaybackTests: XCTestCase {
    func testPlaybackRejectsMissingFile() {
        let expectation = expectation(description: "playback completion")
        AppleAudioPlayback().play(fileURL: URL(fileURLWithPath: "/tmp/missing-recording.m4a")) { result in
            if case .success = result {
                XCTFail("不存在的文件不应进入播放成功状态")
            }
            expectation.fulfill()
        }
        wait(for: [expectation], timeout: 1)
    }
}
