import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

final class CoreProcessorTests: XCTestCase {
    func testResampleReturnsExpectedSamplesAndMetadata() throws {
        let input = pcmData([0, 1_000, 2_000, 3_000])

        let output = try CoreProcessor().resample(
            pcmData: input,
            sampleRate: 4,
            channels: 1,
            targetSampleRate: 2,
            timestampMs: 42
        )

        XCTAssertEqual(samples(from: output.pcmData), [0, 2_000])
        XCTAssertEqual(output.sampleRate, 2)
        XCTAssertEqual(output.channels, 1)
        XCTAssertEqual(output.timestampMs, 42)
    }

    func testResampleMapsInvalidPCMToTypedError() {
        XCTAssertThrowsError(
            try CoreProcessor().resample(
                pcmData: Data([0x00, 0x01, 0x02]),
                sampleRate: 16_000,
                channels: 1,
                targetSampleRate: 8_000,
                timestampMs: 0
            )
        ) { error in
            XCTAssertEqual(error as? CoreProcessorError, .invalidPCM)
        }
    }

    private func pcmData(_ samples: [Int16]) -> Data {
        samples.withUnsafeBufferPointer { Data(buffer: $0) }
    }

    private func samples(from data: Data) -> [Int16] {
        data.withUnsafeBytes { Array($0.bindMemory(to: Int16.self)) }
    }
}
