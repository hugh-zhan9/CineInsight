import Combine
import Foundation

public struct DaemonLaunchConfiguration: Equatable {
    public let executablePath: String
    public let port: Int
    public let token: String

    public init(executablePath: String, port: Int, token: String) {
        self.executablePath = executablePath
        self.port = port
        self.token = token
    }

    public var baseURL: URL {
        URL(string: "http://127.0.0.1:\(port)")!
    }

    public var authorizationHeader: String {
        "Bearer \(token)"
    }
}

public enum DaemonLifecycleState: String, Equatable {
    case stopped
    case starting
    case running
    case failed
}

public final class DaemonLifecycleManager: ObservableObject {
    @Published public private(set) var state: DaemonLifecycleState
    @Published public private(set) var health: DaemonHealth?
    @Published public private(set) var message: String
    private var process: Process?

    public init(
        state: DaemonLifecycleState = .stopped,
        health: DaemonHealth? = nil,
        message: String = "Daemon stopped"
    ) {
        self.state = state
        self.health = health
        self.message = message
    }

    @MainActor
    public func launch(_ configuration: DaemonLaunchConfiguration) {
        guard process == nil else { return }
        guard let executablePath = resolveExecutablePath(configuration.executablePath) else {
            message = "Daemon executable not found: \(configuration.executablePath)"
            return
        }

        let daemon = Process()
        daemon.executableURL = URL(fileURLWithPath: executablePath)
        daemon.environment = ProcessInfo.processInfo.environment.merging([
            "CINE_DAEMON_PORT": "\(configuration.port)",
            "CINE_DAEMON_TOKEN": configuration.token
        ]) { _, new in new }
        do {
            try daemon.run()
            process = daemon
            state = .starting
            message = "Daemon process started"
        } catch {
            state = .failed
            message = error.localizedDescription
        }
    }

    private func resolveExecutablePath(_ configuredPath: String) -> String? {
        if configuredPath.contains("/") {
            return FileManager.default.isExecutableFile(atPath: configuredPath) ? configuredPath : nil
        }
        for directory in (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":") {
            let candidate = URL(fileURLWithPath: String(directory)).appendingPathComponent(configuredPath).path
            if FileManager.default.isExecutableFile(atPath: candidate) {
                return candidate
            }
        }
        return nil
    }

    @MainActor
    public func stop() {
        process?.terminate()
        process = nil
        state = .stopped
        health = nil
        message = "Daemon stopped"
    }

    @MainActor
    public func refreshHealth(using client: NativeAPIClient) async {
        state = .starting
        message = "Checking daemon health"
        do {
            health = try await client.health()
            state = .running
            message = "Daemon running"
        } catch {
            state = .failed
            message = error.localizedDescription
        }
    }
}

public enum NativeAPIError: LocalizedError, Equatable {
    case invalidURL(String)
    case httpStatus(Int)
    case emptyResponse

    public var errorDescription: String? {
        switch self {
        case .invalidURL(let path):
            return "Invalid daemon URL: \(path)"
        case .httpStatus(let code):
            return "Daemon returned HTTP \(code)"
        case .emptyResponse:
            return "Daemon returned an empty response"
        }
    }
}

public struct EmptyRequest: Encodable, Equatable {
    public init() {}
}

public struct VideoFilterRequest: Encodable, Equatable {
    public let keyword: String?
    public let tagIds: [Int64]
    public let minSize: Int64?
    public let maxSize: Int64?
    public let minHeight: Int?
    public let maxHeight: Int?
    public let cursor: VideoCursor?
    public let limit: Int?

    public init(
        keyword: String? = nil,
        tagIds: [Int64] = [],
        minSize: Int64? = nil,
        maxSize: Int64? = nil,
        minHeight: Int? = nil,
        maxHeight: Int? = nil,
        cursor: VideoCursor? = nil,
        limit: Int? = nil
    ) {
        self.keyword = keyword
        self.tagIds = tagIds
        self.minSize = minSize
        self.maxSize = maxSize
        self.minHeight = minHeight
        self.maxHeight = maxHeight
        self.cursor = cursor
        self.limit = limit
    }
}

public struct RenameVideoRequest: Encodable, Equatable {
    public let name: String

    public init(name: String) {
        self.name = name
    }
}

public struct DeleteVideoRequest: Encodable, Equatable {
    public let deleteFile: Bool

    public init(deleteFile: Bool = false) {
        self.deleteFile = deleteFile
    }
}

public struct TagMutationRequest: Encodable, Equatable {
    public let name: String
    public let color: String

    public init(name: String, color: String = "") {
        self.name = name
        self.color = color
    }
}

public struct VideoTagMutationRequest: Encodable, Equatable {
    public let tagId: Int64

    public init(tagId: Int64) {
        self.tagId = tagId
    }
}

public struct ScanDirectoryMutationRequest: Encodable, Equatable {
    public let path: String
    public let alias: String

    public init(path: String, alias: String = "") {
        self.path = path
        self.alias = alias
    }
}

public struct ScanDirectoryRequest: Encodable, Equatable {
    public let path: String
    public let extensions: String?

    public init(path: String, extensions: String? = nil) {
        self.path = path
        self.extensions = extensions
    }
}

public struct AddVideoRequest: Encodable, Equatable {
    public let path: String

    public init(path: String) {
        self.path = path
    }
}

public struct ShortFeedFeedbackRequest: Encodable, Equatable {
    public let liked: Bool?
    public let favorited: Bool?
    public let viewed: Bool

    public init(liked: Bool? = nil, favorited: Bool? = nil, viewed: Bool = false) {
        self.liked = liked
        self.favorited = favorited
        self.viewed = viewed
    }
}

public struct CleanupAnalyzeRequest: Encodable, Equatable {
    public let maxDurationSeconds: Double
    public let minWidth: Int
    public let minHeight: Int

    public init(maxDurationSeconds: Double = 60, minWidth: Int = 640, minHeight: Int = 360) {
        self.maxDurationSeconds = maxDurationSeconds
        self.minWidth = minWidth
        self.minHeight = minHeight
    }
}

public final class NativeAPIClient {
    public let configuration: DaemonLaunchConfiguration
    private let session: URLSession
    private let decoder: JSONDecoder
    private let encoder: JSONEncoder

    public init(
        configuration: DaemonLaunchConfiguration,
        session: URLSession = .shared,
        decoder: JSONDecoder = .cineInsight,
        encoder: JSONEncoder = .cineInsight
    ) {
        self.configuration = configuration
        self.session = session
        self.decoder = decoder
        self.encoder = encoder
    }

    public func health() async throws -> DaemonHealth {
        try await get("/health")
    }

    public func listVideos(limit: Int = 80) async throws -> VideoListResponse {
        try await post("/api/videos/search", body: VideoFilterRequest(limit: limit))
    }

    public func searchVideos(_ filter: VideoFilterRequest) async throws -> VideoListResponse {
        try await post("/api/videos/search", body: filter)
    }

    public func randomCandidate() async throws -> RandomCandidateResponse {
        try await post("/api/videos/random-candidate", body: EmptyRequest())
    }

    public func scanDirectory(path: String, extensions: String? = nil) async throws -> ScanDirectoryResponse {
        try await post("/api/videos/scan", body: ScanDirectoryRequest(path: path, extensions: extensions))
    }

    public func addVideo(path: String) async throws -> VideoMutationResponse {
        try await post("/api/videos/add", body: AddVideoRequest(path: path))
    }

    public func renameVideo(id: Int64, name: String) async throws -> VideoMutationResponse {
        try await post("/api/videos/\(id)/rename", body: RenameVideoRequest(name: name))
    }

    public func deleteVideo(id: Int64, deleteFile: Bool = false) async throws -> VideoMutationResponse {
        try await post("/api/videos/\(id)/delete", body: DeleteVideoRequest(deleteFile: deleteFile))
    }

    public func previewSession(videoId: Int64) async throws -> PreviewSessionResponse {
        try await get("/api/videos/\(videoId)/preview-session")
    }

    public func previewExternally(videoId: Int64) async throws {
        try await postNoContent("/api/videos/\(videoId)/preview-externally", body: EmptyRequest())
    }

    public func playVideo(id: Int64) async throws -> PlaybackAttemptResponse {
        try await post("/api/videos/\(id)/play", body: EmptyRequest())
    }

    public func playRandomVideo() async throws -> PlaybackAttemptResponse {
        try await post("/api/videos/random-play", body: EmptyRequest())
    }

    public func listTags() async throws -> TagListResponse {
        try await get("/api/tags")
    }

    public func createTag(name: String, color: String = "") async throws -> TagRecord {
        try await post("/api/tags", body: TagMutationRequest(name: name, color: color))
    }

    public func updateTag(id: Int64, name: String, color: String = "") async throws -> TagRecord {
        try await post("/api/tags/\(id)", body: TagMutationRequest(name: name, color: color))
    }

    public func deleteTag(id: Int64) async throws {
        try await postNoContent("/api/tags/\(id)/delete", body: EmptyRequest())
    }

    public func assignTag(videoId: Int64, tagId: Int64) async throws {
        try await postNoContent("/api/videos/\(videoId)/tags", body: VideoTagMutationRequest(tagId: tagId))
    }

    public func removeTag(videoId: Int64, tagId: Int64) async throws {
        try await postNoContent("/api/videos/\(videoId)/tags/delete", body: VideoTagMutationRequest(tagId: tagId))
    }

    public func settings() async throws -> PublicSettings {
        try await get("/api/settings")
    }

    public func listScanDirectories() async throws -> ScanDirectoryListResponse {
        try await get("/api/scan-directories")
    }

    public func addScanDirectory(path: String, alias: String = "") async throws -> ScanDirectoryRecord {
        try await post("/api/scan-directories", body: ScanDirectoryMutationRequest(path: path, alias: alias))
    }

    public func updateScanDirectory(id: Int64, path: String, alias: String = "") async throws -> ScanDirectoryRecord {
        try await post("/api/scan-directories/\(id)", body: ScanDirectoryMutationRequest(path: path, alias: alias))
    }

    public func deleteScanDirectory(id: Int64) async throws {
        try await postNoContent("/api/scan-directories/\(id)/delete", body: EmptyRequest())
    }

    public func searchSubtitles(keyword: String) async throws -> SubtitleSearchResponse {
        let item = URLQueryItem(name: "keyword", value: keyword)
        var components = URLComponents(string: "/api/subtitles/search")!
        components.queryItems = [item]
        return try await get(components.string ?? "/api/subtitles/search")
    }

    public func listAITagCandidates() async throws -> AITagCandidateListResponse {
        try await get("/api/ai-tags/candidates")
    }

    public func approveAITagCandidate(id: Int64) async throws -> AITagCandidateRecord {
        try await post("/api/ai-tags/candidates/\(id)/approve", body: EmptyRequest())
    }

    public func rejectAITagCandidate(id: Int64) async throws -> AITagCandidateRecord {
        try await post("/api/ai-tags/candidates/\(id)/reject", body: EmptyRequest())
    }

    public func nextShortFeedVideo() async throws -> ShortFeedVideoRecord {
        try await get("/api/short-feed/next")
    }

    public func recordShortFeedFeedback(videoId: Int64, request: ShortFeedFeedbackRequest) async throws -> ShortFeedInteractionRecord {
        try await post("/api/short-feed/videos/\(videoId)/feedback", body: request)
    }

    public func analyzeCleanup(request: CleanupAnalyzeRequest = CleanupAnalyzeRequest()) async throws -> CleanupAnalysisRecord {
        try await post("/api/cleanup/analyze", body: request)
    }

    public func diagnostics() async throws -> DiagnosticsSnapshot {
        try await get("/api/diagnostics")
    }

    public func absoluteURL(for locator: String) -> URL? {
        URL(string: locator, relativeTo: configuration.baseURL)?.absoluteURL
    }

    private func get<Response: Decodable>(_ path: String) async throws -> Response {
        var request = try makeRequest(path: path)
        request.httpMethod = "GET"
        return try await send(request)
    }

    private func post<RequestBody: Encodable, Response: Decodable>(_ path: String, body: RequestBody) async throws -> Response {
        var request = try makeRequest(path: path)
        request.httpMethod = "POST"
        request.httpBody = try encoder.encode(body)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        return try await send(request)
    }

    private func postNoContent<RequestBody: Encodable>(_ path: String, body: RequestBody) async throws {
        var request = try makeRequest(path: path)
        request.httpMethod = "POST"
        request.httpBody = try encoder.encode(body)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        _ = try await sendRaw(request)
    }

    private func makeRequest(path: String) throws -> URLRequest {
        guard let url = URL(string: path, relativeTo: configuration.baseURL)?.absoluteURL else {
            throw NativeAPIError.invalidURL(path)
        }
        var request = URLRequest(url: url)
        request.setValue(configuration.authorizationHeader, forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        return request
    }

    private func send<Response: Decodable>(_ request: URLRequest) async throws -> Response {
        let data = try await sendRaw(request)
        if data.isEmpty {
            throw NativeAPIError.emptyResponse
        }
        return try decoder.decode(Response.self, from: data)
    }

    private func sendRaw(_ request: URLRequest) async throws -> Data {
        let (data, response) = try await session.data(for: request)
        if let http = response as? HTTPURLResponse, !(200...299).contains(http.statusCode) {
            throw NativeAPIError.httpStatus(http.statusCode)
        }
        return data
    }
}

public struct DaemonHealth: Decodable, Equatable {
    public let service: String
    public let status: String
    public let version: String
    public let appCompatVersion: String
    public let schema: SchemaHealth
    public let database: DatabaseHealth
}

public struct SchemaHealth: Decodable, Equatable {
    public let status: String
    public let requiredTables: [String]
    public let missingTables: [String]
}

public struct DatabaseHealth: Decodable, Equatable {
    public let configured: Bool
    public let connected: Bool
    public let host: String?
    public let database: String?
    public let error: String?
}

public struct VideoTagSummary: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let name: String
    public let color: String

    public init(id: Int64, name: String, color: String) {
        self.id = id
        self.name = name
        self.color = color
    }
}

public struct VideoSummary: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let name: String
    public let path: String
    public let directory: String
    public let size: Int64
    public let duration: Double
    public let resolution: String
    public let width: Int
    public let height: Int
    public let isStale: Bool
    public let playCount: Int
    public let randomPlayCount: Int
    public let lastPlayedAt: String?
    public let tags: [VideoTagSummary]
    public let createdAt: String?
    public let updatedAt: String?
    public let score: Double

    public init(
        id: Int64,
        name: String,
        path: String,
        directory: String,
        size: Int64,
        duration: Double,
        resolution: String,
        width: Int,
        height: Int,
        isStale: Bool,
        playCount: Int,
        randomPlayCount: Int,
        lastPlayedAt: String?,
        tags: [VideoTagSummary],
        createdAt: String?,
        updatedAt: String?,
        score: Double
    ) {
        self.id = id
        self.name = name
        self.path = path
        self.directory = directory
        self.size = size
        self.duration = duration
        self.resolution = resolution
        self.width = width
        self.height = height
        self.isStale = isStale
        self.playCount = playCount
        self.randomPlayCount = randomPlayCount
        self.lastPlayedAt = lastPlayedAt
        self.tags = tags
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.score = score
    }
}

public struct VideoCursor: Codable, Equatable {
    public let score: Double
    public let size: Int64
    public let id: Int64

    public init(score: Double, size: Int64, id: Int64) {
        self.score = score
        self.size = size
        self.id = id
    }
}

public struct RandomCandidateResponse: Decodable, Equatable {
    public let video: VideoSummary?
    public let reasonCode: String?
    public let userMessage: String?
}

public struct VideoListResponse: Decodable, Equatable {
    public let videos: [VideoSummary]
    public let nextCursor: VideoCursor?
}

public struct ScannedFileResponse: Decodable, Equatable {
    public let path: String
    public let size: Int64
}

public struct ScanDirectoryResponse: Decodable, Equatable {
    public let files: [ScannedFileResponse]
}

public struct VideoMutationResponse: Decodable, Equatable {
    public let video: VideoSummary?
    public let ok: Bool
    public let reasonCode: String?
    public let userMessage: String?
}

public enum PreviewMode: String, Decodable, Equatable {
    case inline
    case externalPreview = "external-preview"
    case unsupported
}

public struct PreviewSourceDescriptor: Decodable, Equatable {
    public let locatorStrategy: String
    public let locatorValue: String
    public let mime: String
}

public struct PreviewExternalAction: Decodable, Equatable {
    public let actionId: String
    public let buttonLabel: String
    public let hint: String
}

public struct PreviewSessionResponse: Decodable, Equatable {
    public let videoId: Int64
    public let mode: PreviewMode
    public let displayName: String
    public let inlineSource: PreviewSourceDescriptor?
    public let externalAction: PreviewExternalAction?
    public let reasonCode: String?
    public let reasonMessage: String?
}

public struct PlaybackReconcileResult: Decodable, Equatable {
    public let videoId: Int64
    public let didMarkStale: Bool
    public let didRelocate: Bool
    public let didRefreshMetadata: Bool
    public let needsReload: Bool
    public let updatedVideo: VideoSummary?
    public let reasonCode: String?
}

public struct PlaybackAttemptResponse: Decodable, Equatable {
    public let video: VideoSummary?
    public let dispatchSucceeded: Bool
    public let userMessage: String?
    public let reasonCode: String?
    public let reconcileResult: PlaybackReconcileResult?
}

public struct TagRecord: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let name: String
    public let color: String

    public init(id: Int64, name: String, color: String) {
        self.id = id
        self.name = name
        self.color = color
    }
}

public struct TagListResponse: Decodable, Equatable {
    public let tags: [TagRecord]
}

public struct ScanDirectoryRecord: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let path: String
    public let alias: String

    public init(id: Int64, path: String, alias: String) {
        self.id = id
        self.path = path
        self.alias = alias
    }
}

public struct ScanDirectoryListResponse: Decodable, Equatable {
    public let directories: [ScanDirectoryRecord]
}

public struct PublicSettings: Decodable, Equatable {
    public let videoExtensions: String
    public let playWeight: Double
    public let shortFeedMaxDurationMinutes: Int
    public let theme: String
    public let deeplApiKeyConfigured: Bool
    public let aiTaggingApiKeyConfigured: Bool
    public let aiTaggingFrameCount: Int
    public let aiTaggingSubtitleCharLimit: Int
    public let aiTaggingStartupBatchSize: Int

    public init(
        videoExtensions: String,
        playWeight: Double,
        shortFeedMaxDurationMinutes: Int,
        theme: String,
        deeplApiKeyConfigured: Bool,
        aiTaggingApiKeyConfigured: Bool,
        aiTaggingFrameCount: Int,
        aiTaggingSubtitleCharLimit: Int,
        aiTaggingStartupBatchSize: Int
    ) {
        self.videoExtensions = videoExtensions
        self.playWeight = playWeight
        self.shortFeedMaxDurationMinutes = shortFeedMaxDurationMinutes
        self.theme = theme
        self.deeplApiKeyConfigured = deeplApiKeyConfigured
        self.aiTaggingApiKeyConfigured = aiTaggingApiKeyConfigured
        self.aiTaggingFrameCount = aiTaggingFrameCount
        self.aiTaggingSubtitleCharLimit = aiTaggingSubtitleCharLimit
        self.aiTaggingStartupBatchSize = aiTaggingStartupBatchSize
    }
}

public struct SubtitleSegmentRecord: Decodable, Equatable {
    public let index: Int
    public let startTimeMs: Int64
    public let endTimeMs: Int64
    public let text: String
    public let lines: [String]
}

public struct SubtitleSearchMatch: Decodable, Equatable {
    public let video: VideoSummary
    public let segment: SubtitleSegmentRecord
}

public struct SubtitleSearchResponse: Decodable, Equatable {
    public let matches: [SubtitleSearchMatch]
}

public enum AITagCandidateStatus: String, Decodable, Equatable {
    case pending
    case approved
    case rejected
    case superseded
}

public struct AITagCandidateRecord: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let videoId: Int64
    public let suggestedName: String
    public let normalizedName: String
    public let matchedTagId: Int64?
    public let confidence: String
    public let reasoning: String
    public let sourceSummary: String
    public let status: AITagCandidateStatus
}

public struct AITagCandidateListResponse: Decodable, Equatable {
    public let candidates: [AITagCandidateRecord]
}

public struct ShortFeedInteractionRecord: Decodable, Equatable {
    public let videoId: Int64
    public let liked: Bool
    public let favorited: Bool
    public let viewCount: Int
}

public struct ShortFeedVideoRecord: Decodable, Equatable, Identifiable {
    public let id: Int64
    public let name: String
    public let duration: Double
    public let width: Int
    public let height: Int
    public let tags: [VideoTagSummary]
}

public struct CleanupDuplicateGroup: Decodable, Equatable {
    public let originalId: Int64
    public let candidateIds: [Int64]
    public let reason: String
}

public struct CleanupAnalysisRecord: Decodable, Equatable {
    public let duplicateGroups: [CleanupDuplicateGroup]
    public let lowDurationIds: [Int64]
    public let lowResolutionIds: [Int64]
}

public struct DiagnosticsSnapshot: Decodable, Equatable {
    public let videoCount: Int64
    public let tagCount: Int64
    public let subtitleSegmentCount: Int64
    public let aiCandidateCount: Int64
    public let shortFeedInteractionCount: Int64
    public let redactedSettings: PublicSettings
}

public extension JSONDecoder {
    static var cineInsight: JSONDecoder {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return decoder
    }
}

public extension JSONEncoder {
    static var cineInsight: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        return encoder
    }
}
