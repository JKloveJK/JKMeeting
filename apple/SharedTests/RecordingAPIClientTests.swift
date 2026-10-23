import Foundation
import XCTest
#if os(iOS)
@testable import JKMeetingShared_iOS
#elseif os(macOS)
@testable import JKMeetingShared_macOS
#endif

private final class RecordingURLProtocol: URLProtocol, @unchecked Sendable {
    static let lock = NSLock()
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (Int, Data))?

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do {
            Self.lock.lock()
            let handler = Self.handler
            Self.lock.unlock()
            guard let handler else { throw URLError(.badServerResponse) }
            let (code, data) = try handler(request)
            let reply = HTTPURLResponse(url: request.url!, statusCode: code, httpVersion: nil, headerFields: nil)!
            client?.urlProtocol(self, didReceive: reply, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: data)
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }
    override func stopLoading() {}
}

@MainActor
final class RecordingAPIClientTests: XCTestCase {
    private let signed = "http://127.0.0.1:9000/recordings/recordings/8fc71433-a413-4975-8dcc-df9e21ab737f/audio.m4a?X-Amz-Signature=signed"
    private let signedOgg = "http://127.0.0.1:9000/recordings/recordings/8fc71433-a413-4975-8dcc-df9e21ab737f/audio.ogg?X-Amz-Signature=signed"
    private let identifier = "8FC71433-A413-4975-8DCC-DF9E21AB737F"

    private func client() throws -> RecordingAPIClient {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [RecordingURLProtocol.self]
        return try RecordingAPIClient(apiBaseURL: "http://127.0.0.1:8082", storageOrigin: "http://127.0.0.1:9000", token: "test-token", configuration: configuration)
    }

    private func metadata(fileExtension: String = "m4a") throws -> RecordingMetadata {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + "." + fileExtension)
        try Data([1, 2, 3, 4]).write(to: url)
        addTeardownBlock { try? FileManager.default.removeItem(at: url) }
        return RecordingMetadata(fileURL: url, durationMs: 1_000, sizeBytes: 4, sampleRate: 16_000, channels: 1)
    }

    func testUploadIsOptInAndBearerNeverLeaksToS3() async throws {
        try await assertUpload(fileExtension: "m4a", contentType: "audio/mp4", signed: signed)
    }

    func testOggUploadUsesOggContentTypeAndObjectKey() async throws {
        try await assertUpload(fileExtension: "ogg", contentType: "audio/ogg", signed: signedOgg)
    }

    func testOggUploadRejectsM4AObjectKey() async throws {
        let identifier = self.identifier
        let signed = self.signed
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            XCTAssertNotEqual(request.url?.port, 9000)
            return (201, try JSONSerialization.data(withJSONObject: ["recording_id": identifier, "upload_url": signed, "upload_expires_at": "2099-09-30T12:00:00Z"]))
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }
        do {
            _ = try await client().transcribe(metadata(fileExtension: "ogg")) { _ in }
            XCTFail("ogg 录音不应上传到 audio.m4a")
        } catch RecordingAPIError.invalidUploadURL { }
    }

    func testRemotePlaybackResolvesSignedHLSURLAgainstAPIOrigin() async throws {
        let identifier = self.identifier
        let hlsPath = "/v1/hls/\(identifier.lowercased())/master.m3u8?exp=4102444800&sig=abc"
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            XCTAssertEqual(request.url?.path, "/v1/recordings/\(identifier)/playback")
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer test-token")
            return (200, try JSONSerialization.data(withJSONObject: [
                "playback_url": "http://127.0.0.1:9000/x", "transcode_status": "ready",
                "hls_url": hlsPath, "hls_variants_kbps": "32,64,96",
            ]))
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }

        let playback = try await client().remotePlayback(recordingID: identifier)
        XCTAssertTrue(playback.isHLSReady)
        XCTAssertEqual(playback.variantsKbps, [32, 64, 96])
        XCTAssertEqual(playback.hlsURL?.absoluteString, "http://127.0.0.1:8082" + hlsPath)
    }

    func testRemotePlaybackWithoutHLSIsNotReady() throws {
        let base = try XCTUnwrap(URL(string: "http://127.0.0.1:8082"))
        for (status, extra) in [("pending", [String: String]()), ("none", [:]), ("failed", ["transcode_error": "FFmpeg 退出码 1"])] {
            let body = try JSONSerialization.data(withJSONObject: ["transcode_status": status].merging(extra) { $1 })
            let playback = try RecordingAPIClient.remotePlayback(from: body, apiBaseURL: base, recordingID: identifier)
            XCTAssertFalse(playback.isHLSReady, status)
            XCTAssertNil(playback.hlsURL)
            XCTAssertEqual(playback.transcodeError, extra["transcode_error"])
        }
    }

    func testRemotePlaybackRejectsHLSURLOutsideRecordingOrOrigin() throws {
        let base = try XCTUnwrap(URL(string: "http://127.0.0.1:8082"))
        let id = identifier.lowercased()
        for hls in [
            "http://evil.example:8082/v1/hls/\(id)/master.m3u8?sig=x",
            "http://127.0.0.1:9999/v1/hls/\(id)/master.m3u8?sig=x",
            "/v1/hls/\(UUID().uuidString.lowercased())/master.m3u8?sig=x",
            "/v1/hls/\(id)/32k/index.m3u8?sig=x",
            "/v1/hls/\(id)/master.m3u8",
        ] {
            let body = try JSONSerialization.data(withJSONObject: ["transcode_status": "ready", "hls_url": hls, "hls_variants_kbps": "32"])
            XCTAssertThrowsError(try RecordingAPIClient.remotePlayback(from: body, apiBaseURL: base, recordingID: identifier), hls)
        }
    }

    func testUnknownExtensionFailsBeforeAnyRequest() async throws {
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            XCTFail("未知格式不应发出请求：\(request.url?.absoluteString ?? "")")
            return (500, Data())
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }
        do {
            _ = try await client().transcribe(metadata(fileExtension: "wav")) { _ in }
            XCTFail("应拒绝未知扩展名")
        } catch RecordingAPIError.unsupportedAudioFormat(let fileExtension) {
            XCTAssertEqual(fileExtension, "wav")
        }
    }

    private func assertUpload(fileExtension: String, contentType: String, signed: String) async throws {
        let metadata = try metadata(fileExtension: fileExtension)
        let identifier = self.identifier
        let lock = NSLock()
        var seen = [String]()
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            let url = try XCTUnwrap(request.url)
            lock.lock()
            seen.append(request.httpMethod ?? "")
            lock.unlock()
            if url.port == 9000 {
                XCTAssertNil(request.value(forHTTPHeaderField: "Authorization"))
                XCTAssertEqual(request.value(forHTTPHeaderField: "Content-Type"), contentType)
                let body: Data
                if let immediate = request.httpBody {
                    body = immediate
                } else {
                    let stream = try XCTUnwrap(request.httpBodyStream)
                    stream.open()
                    defer { stream.close() }
                    var bytes = [UInt8](repeating: 0, count: 16)
                    let count = stream.read(&bytes, maxLength: bytes.count)
                    body = Data(bytes.prefix(max(0, count)))
                }
                XCTAssertEqual(body, Data([1, 2, 3, 4]))
                return (200, Data())
            }
            XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer test-token")
            if request.httpMethod == "POST" && url.path == "/v1/recordings" {
                let body: Data
                if let immediate = request.httpBody {
                    body = immediate
                } else {
                    let stream = try XCTUnwrap(request.httpBodyStream)
                    stream.open()
                    defer { stream.close() }
                    var bytes = [UInt8](repeating: 0, count: 4_096)
                    let count = stream.read(&bytes, maxLength: bytes.count)
                    XCTAssertGreaterThan(count, 0)
                    body = Data(bytes.prefix(max(0, count)))
                }
                let json = try XCTUnwrap(JSONSerialization.jsonObject(with: body) as? [String: Any])
                XCTAssertEqual(json["duration_ms"] as? Int, 1_000)
                XCTAssertEqual(json["size_bytes"] as? Int, 4)
                XCTAssertEqual(json["content_type"] as? String, contentType)
                return (201, try JSONSerialization.data(withJSONObject: ["recording_id": identifier, "upload_url": signed, "upload_expires_at": "2099-09-30T12:00:00Z"]))
            }
            if url.path.hasSuffix("/complete") { return (200, Data("{}".utf8)) }
            return (200, Data(#"{"status":"done","transcript":"你好","error_message":null}"#.utf8))
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }

        let text = try await client().transcribe(metadata) { _ in }
        XCTAssertEqual(text, "你好")
        lock.withLock { XCTAssertEqual(seen, ["POST", "PUT", "POST", "GET"]) }
    }

    func testRejectsWrongUploadHostBeforeSendingBytes() async throws {
        let identifier = self.identifier
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            XCTAssertNotEqual(request.url?.port, 9000)
            return (201, try JSONSerialization.data(withJSONObject: ["recording_id": identifier, "upload_url": "http://127.0.0.1:9001/recordings/recordings/abc/audio.m4a?X-Amz-Signature=bad", "upload_expires_at": "2099-09-30T12:00:00Z"]))
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }
        do {
            _ = try await client().transcribe(metadata()) { _ in }
            XCTFail("应拒绝不匹配的 S3 端口")
        } catch RecordingAPIError.invalidUploadURL { }
    }

    func testCompleteConflictChecksExistingUploadedStatusInsteadOfCreatingAgain() async throws {
        let identifier = self.identifier
        let signed = self.signed
        RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = { request in
            let url = try XCTUnwrap(request.url)
            if url.port == 9000 { return (200, Data()) }
            if url.path == "/v1/recordings" {
                return (201, try JSONSerialization.data(withJSONObject: ["recording_id": identifier, "upload_url": signed, "upload_expires_at": "2099-09-30T12:00:00Z"]))
            }
            if url.path.hasSuffix("/complete") { return (409, Data()) }
            return (200, Data(#"{"status":"done","transcript":"已完成"}"#.utf8))
        } }
        defer { RecordingURLProtocol.lock.withLock { RecordingURLProtocol.handler = nil } }
        let text = try await client().transcribe(metadata()) { _ in }
        XCTAssertEqual(text, "已完成")
    }

    func testMissingEnvironmentDoesNotUseImplicitCredentials() {
        XCTAssertThrowsError(try RecordingAPIClient(environment: [:]))
    }
}
