import Foundation

public enum TranscriptionPhase: Equatable {
    case unavailable
    case ready
    case uploading
    case transcribing
    case done
    case failed(String)
}

@MainActor
public protocol RecordingTranscribing: AnyObject {
    func transcribe(_ metadata: RecordingMetadata, onStatus: @escaping @MainActor (TranscriptionPhase) -> Void) async throws -> String
}

public enum RecordingAPIError: LocalizedError {
    case invalidConfiguration
    case invalidUploadURL
    case invalidResponse
    case requestFailed(Int)
    case uploadExpired
    case transcriptionFailed(String)
    case timedOut
    case unsupportedAudioFormat(String)

    public var errorDescription: String? {
        switch self {
        case .invalidConfiguration: "服务未配置，请从受信任的开发环境启动 App"
        case .invalidUploadURL: "服务器返回的上传地址不安全或与本地对象存储不一致"
        case .invalidResponse: "服务器返回的数据无效"
        case .requestFailed(let status): "请求失败（HTTP \(status)），录音仍保存在本地"
        case .uploadExpired: "上传地址已过期，请重新发起转写"
        case .transcriptionFailed(let message): "转写失败：\(message)"
        case .timedOut: "转写等待超时，录音仍保存在本地，可重试"
        case .unsupportedAudioFormat(let fileExtension): "不支持的录音格式：\(fileExtension)"
        }
    }
}

/// 服务端回放接口的结果；`hlsURL` 为已签名、可直接交给 AVPlayer 的 master playlist 地址。
public struct RemotePlayback: Equatable, Sendable {
    public let recordingID: String
    public let transcodeStatus: String
    public let hlsURL: URL?
    public let variantsKbps: [Int]
    public let transcodeError: String?

    public var isHLSReady: Bool { transcodeStatus == "ready" && hlsURL != nil && !variantsKbps.isEmpty }
}

@MainActor
public protocol RemotePlaybackProviding: AnyObject {
    var lastRecordingID: String? { get }
    func remotePlayback(recordingID: String) async throws -> RemotePlayback
}

private final class RejectRedirects: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

@MainActor
public final class RecordingAPIClient: RecordingTranscribing, RemotePlaybackProviding {
    public private(set) var lastRecordingID: String?

    private struct Playback: Decodable {
        let transcode_status: String?
        let hls_url: String?
        let hls_variants_kbps: String?
        let transcode_error: String?
    }

    private struct Created: Decodable {
        let recording_id: String
        let upload_url: String
        let upload_expires_at: String
    }

    private struct Status: Decodable {
        let status: String
        let transcript: String?
        let error_message: String?
    }

    private let apiURL: URL
    private let storageOrigin: URL
    private let token: String
    private let session: URLSession
    private let redirectDelegate: RejectRedirects

    public convenience init(environment: [String: String] = ProcessInfo.processInfo.environment) throws {
        guard let api = environment["JK_API_BASE_URL"],
              let storage = environment["JK_S3_ORIGIN"],
              let token = environment["JK_API_BEARER_TOKEN"] else {
            throw RecordingAPIError.invalidConfiguration
        }
        try self.init(apiBaseURL: api, storageOrigin: storage, token: token)
    }

    public init(apiBaseURL: String, storageOrigin: String, token: String, configuration: URLSessionConfiguration = .ephemeral) throws {
        guard let api = URL(string: apiBaseURL), let storage = URL(string: storageOrigin),
              Self.validOrigin(api), Self.validOrigin(storage),
              api.path.isEmpty || api.path == "/", storage.path.isEmpty || storage.path == "/",
              !token.isEmpty, !token.contains(where: { $0.isNewline }) else {
            throw RecordingAPIError.invalidConfiguration
        }
        self.apiURL = api
        self.storageOrigin = storage
        self.token = token
        configuration.timeoutIntervalForRequest = 30
        configuration.timeoutIntervalForResource = 120
        let delegate = RejectRedirects()
        self.redirectDelegate = delegate
        self.session = URLSession(configuration: configuration, delegate: delegate, delegateQueue: nil)
    }

    private static func validOrigin(_ url: URL) -> Bool {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              (components.scheme == "http" || components.scheme == "https"),
              components.host != nil, components.user == nil, components.password == nil,
              components.query == nil, components.fragment == nil,
              let port = components.port, (1...65535).contains(port) else { return false }
        return true
    }

    /// 与服务端白名单一致；未知扩展名直接失败，不默认当作 m4a。
    static func audioFormat(for fileURL: URL) throws -> (fileExtension: String, contentType: String) {
        switch fileURL.pathExtension.lowercased() {
        case "ogg": ("ogg", "audio/ogg")
        case "m4a": ("m4a", "audio/mp4")
        case let other: throw RecordingAPIError.unsupportedAudioFormat(other)
        }
    }

    private func uploadURL(_ value: String, recordingID: String, fileExtension: String) throws -> URL {
        guard let url = URL(string: value), let origin = URLComponents(url: storageOrigin, resolvingAgainstBaseURL: false),
              let signed = URLComponents(url: url, resolvingAgainstBaseURL: false),
              signed.scheme == origin.scheme, signed.host == origin.host, signed.port == origin.port,
              signed.user == nil, signed.password == nil, signed.fragment == nil,
              signed.path == "/recordings/recordings/\(recordingID.lowercased())/audio.\(fileExtension)",
              signed.queryItems?.contains(where: { $0.name == "X-Amz-Signature" && !$0.value.isNilOrEmpty }) == true else {
            throw RecordingAPIError.invalidUploadURL
        }
        return url
    }

    private func apiRequest(_ path: String, method: String) -> URLRequest {
        var request = URLRequest(url: apiURL.appending(path: path))
        request.httpMethod = method
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        return request
    }

    private func response(_ response: URLResponse, expected: Int) throws {
        guard let http = response as? HTTPURLResponse else { throw RecordingAPIError.invalidResponse }
        guard http.statusCode == expected else { throw RecordingAPIError.requestFailed(http.statusCode) }
    }

    private func status(for id: String) async throws -> Status {
        let (data, reply) = try await session.data(for: apiRequest("v1/recordings/\(id)", method: "GET"))
        try response(reply, expected: 200)
        return try JSONDecoder().decode(Status.self, from: data)
    }

    private func put(file: URL, at url: URL, contentType: String) async throws {
        var request = URLRequest(url: url)
        request.httpMethod = "PUT"
        request.setValue(contentType, forHTTPHeaderField: "Content-Type")
        let (_, reply) = try await session.upload(for: request, fromFile: file)
        guard let http = reply as? HTTPURLResponse else { throw RecordingAPIError.invalidResponse }
        guard (200...299).contains(http.statusCode) else { throw RecordingAPIError.requestFailed(http.statusCode) }
    }

    private func confirm(_ id: String) async throws {
        let (_, reply) = try await session.data(for: apiRequest("v1/recordings/\(id)/complete", method: "POST"))
        try response(reply, expected: 200)
    }

    public func transcribe(_ metadata: RecordingMetadata, onStatus: @escaping @MainActor (TranscriptionPhase) -> Void) async throws -> String {
        guard metadata.durationMs > 0, metadata.sizeBytes > 0, metadata.fileURL.isFileURL else {
            throw RecordingAPIError.invalidResponse
        }
        let format = try Self.audioFormat(for: metadata.fileURL)
        onStatus(.uploading)
        var lastError: Error = RecordingAPIError.invalidResponse
        for attempt in 0..<2 {
            try Task.checkCancellation()
            onStatus(.uploading)
            do {
                var create = apiRequest("v1/recordings", method: "POST")
                create.setValue("application/json", forHTTPHeaderField: "Content-Type")
                create.httpBody = try JSONSerialization.data(withJSONObject: [
                    "duration_ms": metadata.durationMs, "size_bytes": metadata.sizeBytes, "content_type": format.contentType
                ])
                let (data, reply) = try await session.data(for: create)
                try response(reply, expected: 201)
                let created = try JSONDecoder().decode(Created.self, from: data)
                guard UUID(uuidString: created.recording_id) != nil else { throw RecordingAPIError.invalidResponse }
                lastRecordingID = created.recording_id
                let signed = try uploadURL(created.upload_url, recordingID: created.recording_id, fileExtension: format.fileExtension)
                guard let expires = ISO8601DateFormatter().date(from: created.upload_expires_at), expires > Date() else {
                    throw RecordingAPIError.uploadExpired
                }
                var uploaded = false
                for _ in 0..<2 {
                    try Task.checkCancellation()
                    guard expires > Date() else { throw RecordingAPIError.uploadExpired }
                    do { try await put(file: metadata.fileURL, at: signed, contentType: format.contentType); uploaded = true; break }
                    catch is CancellationError { throw CancellationError() }
                    catch { lastError = error }
                }
                if !uploaded { throw lastError }
                do {
                    try await confirm(created.recording_id)
                } catch is CancellationError { throw CancellationError() }
                catch {
                    let current = try await status(for: created.recording_id)
                    if current.status == "created" {
                        guard expires > Date() else { throw RecordingAPIError.uploadExpired }
                        try await put(file: metadata.fileURL, at: signed, contentType: format.contentType)
                        try await confirm(created.recording_id)
                    } else if current.status == "failed" {
                        throw RecordingAPIError.transcriptionFailed(current.error_message ?? "服务端处理失败")
                    } else if !["uploaded", "transcribing", "done"].contains(current.status) {
                        throw RecordingAPIError.invalidResponse
                    }
                }
                onStatus(.transcribing)
                for _ in 0..<75 {
                    try Task.checkCancellation()
                    let current = try await status(for: created.recording_id)
                    switch current.status {
                    case "done": return current.transcript ?? ""
                    case "failed": throw RecordingAPIError.transcriptionFailed(current.error_message ?? "服务端处理失败")
                    case "uploaded", "transcribing": try await Task.sleep(for: .seconds(2))
                    default: throw RecordingAPIError.invalidResponse
                    }
                }
                throw RecordingAPIError.timedOut
            } catch is CancellationError {
                throw CancellationError()
            } catch let error as RecordingAPIError {
                switch error {
                case .transcriptionFailed, .invalidConfiguration, .invalidUploadURL, .invalidResponse, .timedOut:
                    throw error
                default:
                    lastError = error
                }
            } catch {
                lastError = error
            }
        }
        if lastError is RecordingAPIError { throw lastError }
        throw RecordingAPIError.requestFailed(0)
    }

    public func remotePlayback(recordingID: String) async throws -> RemotePlayback {
        guard UUID(uuidString: recordingID) != nil else { throw RecordingAPIError.invalidResponse }
        let (data, reply) = try await session.data(for: apiRequest("v1/recordings/\(recordingID)/playback", method: "GET"))
        try response(reply, expected: 200)
        return try Self.remotePlayback(from: data, apiBaseURL: apiURL, recordingID: recordingID)
    }

    /// hls_url 是相对路径；只接受同一 API 源下该录音的 master playlist，防止服务端返回值把播放器引向任意地址。
    static func remotePlayback(from data: Data, apiBaseURL: URL, recordingID: String) throws -> RemotePlayback {
        let payload = try JSONDecoder().decode(Playback.self, from: data)
        let status = payload.transcode_status ?? "none"
        var hlsURL: URL?
        if let raw = payload.hls_url, !raw.isEmpty {
            guard let resolved = URL(string: raw, relativeTo: apiBaseURL)?.absoluteURL,
                  let components = URLComponents(url: resolved, resolvingAgainstBaseURL: false),
                  components.scheme == apiBaseURL.scheme, components.host == apiBaseURL.host, components.port == apiBaseURL.port,
                  components.path == "/v1/hls/\(recordingID.lowercased())/master.m3u8",
                  components.queryItems?.contains(where: { $0.name == "sig" && !$0.value.isNilOrEmpty }) == true else {
                throw RecordingAPIError.invalidResponse
            }
            hlsURL = resolved
        }
        let variants = (payload.hls_variants_kbps ?? "").split(separator: ",").compactMap { Int($0.trimmingCharacters(in: .whitespaces)) }
        return RemotePlayback(recordingID: recordingID, transcodeStatus: status, hlsURL: hlsURL,
                              variantsKbps: variants, transcodeError: payload.transcode_error)
    }
}

private extension Optional where Wrapped == String {
    var isNilOrEmpty: Bool { self?.isEmpty != false }
}
