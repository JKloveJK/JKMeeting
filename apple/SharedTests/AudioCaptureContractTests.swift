import AVFoundation
import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class AudioCaptureContractTests: XCTestCase {
    func testPermissionMapperMapsAuthorizedToGranted() {
        XCTAssertEqual(MicrophonePermission.map(isAuthorized: true), .granted)
    }

    func testPermissionMapperMapsDeniedToDenied() {
        XCTAssertEqual(MicrophonePermission.map(isAuthorized: false), .denied)
    }

    func testCapturedFramePreservesPCMMetadata() {
        let frame = CapturedPCMFrame(
            pcmData: Data([0, 0, 1, 0]),
            sampleRate: 48_000,
            channels: 1,
            timestampMs: 42
        )

        XCTAssertEqual(frame.pcmData, Data([0, 0, 1, 0]))
        XCTAssertEqual(frame.sampleRate, 48_000)
        XCTAssertEqual(frame.channels, 1)
        XCTAssertEqual(frame.timestampMs, 42)
    }

    func testTapInstallationUsesInputNodeNativeFormat() {
        XCTAssertNil(AppleAudioCapture.tapInstallationFormat)
    }

    func testLifecycleRejectsSecondStartUntilPreviousCaptureStops() {
        let lifecycle = AudioCaptureLifecycle()

        XCTAssertTrue(lifecycle.beginStart())
        XCTAssertFalse(lifecycle.beginStart())
        lifecycle.markRunning()
        let firstGeneration = lifecycle.generation
        XCTAssertTrue(lifecycle.beginStop())
        XCTAssertTrue(lifecycle.canDeliver(generation: firstGeneration))
        XCTAssertFalse(lifecycle.beginStart())
        lifecycle.markStopped()
        XCTAssertFalse(lifecycle.canDeliver(generation: firstGeneration))
        XCTAssertTrue(lifecycle.beginStart())
        lifecycle.markRunning()
        XCTAssertFalse(lifecycle.isRunning(generation: firstGeneration))
        XCTAssertTrue(lifecycle.isRunning(generation: lifecycle.generation))
    }

    func testNativeFloatBufferConvertsToMonoInt16ForCore() throws {
        let format = try XCTUnwrap(AVAudioFormat(
            commonFormat: .pcmFormatFloat32,
            sampleRate: 48_000,
            channels: 1,
            interleaved: false
        ))
        let buffer = try XCTUnwrap(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 2))
        buffer.frameLength = 2
        buffer.floatChannelData?[0][0] = 0.5
        buffer.floatChannelData?[0][1] = -0.5

        let converter = try NativePCMFrameConverter(sourceFormat: format)
        let frame = try converter.convert(buffer: buffer, timestampMs: 42)
        let samples = frame.pcmData.withUnsafeBytes { Array($0.bindMemory(to: Int16.self)) }

        XCTAssertEqual(frame.sampleRate, 48_000)
        XCTAssertEqual(frame.channels, 1)
        XCTAssertEqual(frame.timestampMs, 42)
        XCTAssertEqual(samples.count, 2)
        XCTAssertEqual(samples[0], 16_384, accuracy: 1)
        XCTAssertEqual(samples[1], -16_384, accuracy: 1)
    }
}
