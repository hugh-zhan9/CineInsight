import Combine
import Foundation

public struct DaemonLaunchConfiguration: Equatable, Sendable {
    public let executablePath: String
    public let port: Int
    public let token: String
    public let shortFeedAssetsPath: String?

    public init(executablePath: String, port: Int, token: String, shortFeedAssetsPath: String? = nil) {
        self.executablePath = executablePath
        self.port = port
        self.token = token
        self.shortFeedAssetsPath = shortFeedAssetsPath
    }

    public var baseURL: URL {
        URL(string: "http://127.0.0.1:\(port)")!
    }

    public var authorizationHeader: String {
        "Bearer \(token)"
    }

    public static func defaultConfiguration(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        bundleResourceURL: URL? = Bundle.main.resourceURL
    ) -> DaemonLaunchConfiguration {
        let port = Int(environment["CINE_DAEMON_PORT"] ?? "") ?? 18088
        let token = environment["CINE_DAEMON_TOKEN"] ?? "dev-token"
        let bundledDaemon = bundleResourceURL?.appendingPathComponent("bin/cine-daemon").path
        let executable = environment["CINE_DAEMON_PATH"]
            ?? {
                guard let bundledDaemon, FileManager.default.isExecutableFile(atPath: bundledDaemon) else {
                    return nil
                }
                return bundledDaemon
            }()
            ?? "cine-daemon"
        let bundleShortFeedAssets = bundleResourceURL?.appendingPathComponent("short-feed").path
        let shortFeedAssets = environment["CINE_SHORT_FEED_ASSETS_DIR"] ?? bundleShortFeedAssets
        return DaemonLaunchConfiguration(
            executablePath: executable,
            port: port,
            token: token,
            shortFeedAssetsPath: shortFeedAssets
        )
    }
}

public enum DaemonLifecycleState: String, Equatable, Sendable {
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
        var environment = ProcessInfo.processInfo.environment.merging([
            "CINE_DAEMON_PORT": "\(configuration.port)",
            "CINE_DAEMON_TOKEN": configuration.token
        ]) { _, new in new }
        if let shortFeedAssetsPath = configuration.shortFeedAssetsPath {
            environment["CINE_SHORT_FEED_ASSETS_DIR"] = shortFeedAssetsPath
        }
        daemon.environment = environment
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

public enum NativeAPIError: LocalizedError, Equatable, Sendable {
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

public struct EmptyRequest: Encodable, Equatable, Sendable {
    public init() {}
}

public struct VideoFilterRequest: Encodable, Equatable, Sendable {
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

public enum VideoSizeFilter: String, CaseIterable, Identifiable, Sendable {
    case all
    case small
    case medium
    case large

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .all: return "Size: All"
        case .small: return "Under 300 MB"
        case .medium: return "300 MB - 1 GB"
        case .large: return "Over 1 GB"
        }
    }

    public var requestBounds: (minSize: Int64?, maxSize: Int64?) {
        switch self {
        case .all:
            return (nil, nil)
        case .small:
            return (nil, 300 * 1_024 * 1_024)
        case .medium:
            return (300 * 1_024 * 1_024, 1_073_741_823)
        case .large:
            return (1_073_741_824, nil)
        }
    }
}

public enum VideoResolutionFilter: String, CaseIterable, Identifiable, Sendable {
    case all
    case sd
    case hd
    case fullHD
    case ultraHD

    public var id: String { rawValue }

    public var label: String {
        switch self {
        case .all: return "Resolution: All"
        case .sd: return "Below 720p"
        case .hd: return "720p"
        case .fullHD: return "1080p"
        case .ultraHD: return "4K+"
        }
    }

    public var requestBounds: (minHeight: Int?, maxHeight: Int?) {
        switch self {
        case .all:
            return (nil, nil)
        case .sd:
            return (nil, 719)
        case .hd:
            return (720, 1079)
        case .fullHD:
            return (1080, 2159)
        case .ultraHD:
            return (2160, nil)
        }
    }
}

public struct RenameVideoRequest: Encodable, Equatable, Sendable {
    public let name: String

    public init(name: String) {
        self.name = name
    }
}

public struct DeleteVideoRequest: Encodable, Equatable, Sendable {
    public let deleteFile: Bool

    public init(deleteFile: Bool = false) {
        self.deleteFile = deleteFile
    }
}

public struct RelocateVideoRequest: Encodable, Equatable, Sendable {
    public let path: String

    public init(path: String) {
        self.path = path
    }
}

public struct BatchVideoRequest: Encodable, Equatable, Sendable {
    public let videoIds: [Int64]
    public let deleteFile: Bool

    public init(videoIds: [Int64], deleteFile: Bool = false) {
        self.videoIds = videoIds
        self.deleteFile = deleteFile
    }
}

public struct BatchVideoTagRequest: Encodable, Equatable, Sendable {
    public let videoIds: [Int64]
    public let tagId: Int64

    public init(videoIds: [Int64], tagId: Int64) {
        self.videoIds = videoIds
        self.tagId = tagId
    }
}

public struct TagMutationRequest: Encodable, Equatable, Sendable {
    public let name: String
    public let color: String

    public init(name: String, color: String = "") {
        self.name = name
        self.color = color
    }
}

public struct VideoTagMutationRequest: Encodable, Equatable, Sendable {
    public let tagId: Int64

    public init(tagId: Int64) {
        self.tagId = tagId
    }
}

public struct ScanDirectoryMutationRequest: Encodable, Equatable, Sendable {
    public let path: String
    public let alias: String

    public init(path: String, alias: String = "") {
        self.path = path
        self.alias = alias
    }
}

public struct ScanDirectoryRequest: Encodable, Equatable, Sendable {
    public let path: String
    public let extensions: String?

    public init(path: String, extensions: String? = nil) {
        self.path = path
        self.extensions = extensions
    }
}

public struct AddVideoRequest: Encodable, Equatable, Sendable {
    public let path: String

    public init(path: String) {
        self.path = path
    }
}

public struct ShortFeedFeedbackRequest: Encodable, Equatable, Sendable {
    public let liked: Bool?
    public let favorited: Bool?
    public let viewed: Bool

    public init(liked: Bool? = nil, favorited: Bool? = nil, viewed: Bool = false) {
        self.liked = liked
        self.favorited = favorited
        self.viewed = viewed
    }
}

public struct FrontendLogRequest: Encodable, Equatable, Sendable {
    public let level: String
    public let source: String
    public let message: String

    public init(level: String, source: String, message: String) {
        self.level = level
        self.source = source
        self.message = message
    }
}

public struct CleanupAnalyzeRequest: Encodable, Equatable, Sendable {
    public let maxDurationSeconds: Double
    public let minWidth: Int
    public let minHeight: Int

    public init(maxDurationSeconds: Double = 60, minWidth: Int = 640, minHeight: Int = 360) {
        self.maxDurationSeconds = maxDurationSeconds
        self.minWidth = minWidth
        self.minHeight = minHeight
    }
}

public struct SettingsUpdateRequest: Encodable, Equatable, Sendable {
    public let confirmBeforeDelete: Bool
    public let deleteOriginalFile: Bool
    public let videoExtensions: String
    public let playWeight: Double
    public let autoScanOnStartup: Bool
    public let shortFeedMaxDurationMinutes: Int
    public let theme: String
    public let logEnabled: Bool
    public let bilingualEnabled: Bool
    public let bilingualLang: String
    public let deeplApiKey: String
    public let aiTaggingBaseUrl: String
    public let aiTaggingApiKey: String
    public let aiTaggingModel: String
    public let aiTaggingFrameCount: Int
    public let aiTaggingSubtitleCharLimit: Int
    public let aiTaggingStartupBatchSize: Int

    public init(
        confirmBeforeDelete: Bool = true,
        deleteOriginalFile: Bool = false,
        videoExtensions: String,
        playWeight: Double,
        autoScanOnStartup: Bool = true,
        shortFeedMaxDurationMinutes: Int,
        theme: String,
        logEnabled: Bool = false,
        bilingualEnabled: Bool = false,
        bilingualLang: String = "zh",
        deeplApiKey: String = "",
        aiTaggingBaseUrl: String = "",
        aiTaggingApiKey: String = "",
        aiTaggingModel: String = "",
        aiTaggingFrameCount: Int,
        aiTaggingSubtitleCharLimit: Int,
        aiTaggingStartupBatchSize: Int
    ) {
        self.confirmBeforeDelete = confirmBeforeDelete
        self.deleteOriginalFile = deleteOriginalFile
        self.videoExtensions = videoExtensions
        self.playWeight = playWeight
        self.autoScanOnStartup = autoScanOnStartup
        self.shortFeedMaxDurationMinutes = shortFeedMaxDurationMinutes
        self.theme = theme
        self.logEnabled = logEnabled
        self.bilingualEnabled = bilingualEnabled
        self.bilingualLang = bilingualLang
        self.deeplApiKey = deeplApiKey
        self.aiTaggingBaseUrl = aiTaggingBaseUrl
        self.aiTaggingApiKey = aiTaggingApiKey
        self.aiTaggingModel = aiTaggingModel
        self.aiTaggingFrameCount = aiTaggingFrameCount
        self.aiTaggingSubtitleCharLimit = aiTaggingSubtitleCharLimit
        self.aiTaggingStartupBatchSize = aiTaggingStartupBatchSize
    }
}

public final class NativeAPIClient: @unchecked Sendable {
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

    public func logFrontend(level: String, source: String, message: String) async throws {
        try await postNoContent(
            "/api/logs/frontend",
            body: FrontendLogRequest(level: level, source: source, message: message)
        )
    }

    public func listVideos(limit: Int = 80) async throws -> VideoListResponse {
        try await post("/api/videos/search", body: VideoFilterRequest(limit: limit))
    }

    public func searchVideos(_ filter: VideoFilterRequest) async throws -> VideoListResponse {
        try await post("/api/videos/search", body: filter)
    }

    public func videosByDirectory(path: String) async throws -> VideoListResponse {
        var components = URLComponents(string: "/api/videos/by-directory")!
        components.queryItems = [URLQueryItem(name: "path", value: path)]
        return try await get(components.string ?? "/api/videos/by-directory")
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

    public func relocateVideo(id: Int64, path: String) async throws -> VideoMutationResponse {
        try await post("/api/videos/\(id)/relocate", body: RelocateVideoRequest(path: path))
    }

    public func refreshVideoMetadata(id: Int64) async throws -> VideoMutationResponse {
        try await post("/api/videos/\(id)/refresh-metadata", body: EmptyRequest())
    }

    public func deleteVideo(id: Int64, deleteFile: Bool = false) async throws -> VideoMutationResponse {
        try await post("/api/videos/\(id)/delete", body: DeleteVideoRequest(deleteFile: deleteFile))
    }

    public func batchDeleteVideos(ids: [Int64], deleteFile: Bool = false) async throws -> BatchVideoOperationResult {
        try await post("/api/videos/batch/delete", body: BatchVideoRequest(videoIds: ids, deleteFile: deleteFile))
    }

    public func batchAddTag(videoIds: [Int64], tagId: Int64) async throws -> BatchVideoOperationResult {
        try await post("/api/videos/batch/tags/add", body: BatchVideoTagRequest(videoIds: videoIds, tagId: tagId))
    }

    public func batchRemoveTag(videoIds: [Int64], tagId: Int64) async throws -> BatchVideoOperationResult {
        try await post("/api/videos/batch/tags/remove", body: BatchVideoTagRequest(videoIds: videoIds, tagId: tagId))
    }

    public func batchRefreshVideoMetadata(ids: [Int64]) async throws -> BatchVideoOperationResult {
        try await post("/api/videos/batch/refresh-metadata", body: BatchVideoRequest(videoIds: ids))
    }

    public func openDirectory(videoId: Int64) async throws {
        try await postNoContent("/api/videos/\(videoId)/open-directory", body: EmptyRequest())
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

    public func updateSettings(_ request: SettingsUpdateRequest) async throws -> PublicSettings {
        try await post("/api/settings", body: request)
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

    public func syncScanDirectories() async throws -> ScanSyncResponse {
        try await post("/api/scan-directories/sync", body: EmptyRequest())
    }

    public func subtitleEngineStatuses() async throws -> [SubtitleEngineStatus] {
        try await get("/api/subtitles/engines")
    }

    public func prepareSubtitleEngine(_ engine: SubtitleEngine) async throws {
        try await postNoContent("/api/subtitles/prepare", body: SubtitlePrepareRequest(engine: engine))
    }

    public func subtitleDependencies() async throws -> [String: Bool] {
        try await get("/api/subtitles/dependencies")
    }

    public func downloadSubtitleDependencies() async throws {
        try await postNoContent("/api/subtitles/dependencies/download", body: EmptyRequest())
    }

    public func generateSubtitle(_ request: SubtitleGenerateRequest) async throws -> SubtitleGenerateResult {
        try await post("/api/subtitles/generate", body: request)
    }

    public func forceGenerateSubtitle(_ request: SubtitleGenerateRequest) async throws -> SubtitleGenerateResult {
        try await post("/api/subtitles/force-generate", body: request)
    }

    public func subtitleStatus() async throws -> SubtitleJobStatus {
        try await get("/api/subtitles/status")
    }

    public func cancelSubtitle() async throws {
        try await postNoContent("/api/subtitles/cancel", body: EmptyRequest())
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

    public func rejectAITagCandidatesByVideo(videoId: Int64) async throws -> RejectAITagCandidatesByVideoResponse {
        try await post("/api/ai-tags/videos/\(videoId)/reject-pending", body: EmptyRequest())
    }

    public func retryAITagging(videoId: Int64) async throws {
        try await postNoContent("/api/ai-tags/videos/\(videoId)/retry", body: EmptyRequest())
    }

    public func aiTaggingStatusSummary() async throws -> AITaggingStatusSummary {
        try await get("/api/ai-tags/status-summary")
    }

    public func shortFeedStatus() async throws -> ShortFeedServerStatus {
        try await get("/api/short-feed/status")
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

    public func startCleanup(request: CleanupAnalyzeRequest = CleanupAnalyzeRequest()) async throws -> CleanupStatus {
        try await post("/api/cleanup/start", body: request)
    }

    public func cleanupStatus() async throws -> CleanupStatus {
        try await get("/api/cleanup/status")
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

public struct DaemonHealth: Decodable, Equatable, Sendable {
    public let service: String
    public let status: String
    public let version: String
    public let appCompatVersion: String
    public let schema: SchemaHealth
    public let database: DatabaseHealth
}

public struct SchemaHealth: Decodable, Equatable, Sendable {
    public let status: String
    public let requiredTables: [String]
    public let missingTables: [String]
}

public struct DatabaseHealth: Decodable, Equatable, Sendable {
    public let configured: Bool
    public let connected: Bool
    public let host: String?
    public let database: String?
    public let error: String?
}

public struct VideoTagSummary: Decodable, Equatable, Identifiable, Sendable {
    public let id: Int64
    public let name: String
    public let color: String

    public init(id: Int64, name: String, color: String) {
        self.id = id
        self.name = name
        self.color = color
    }
}

public struct VideoSummary: Decodable, Equatable, Identifiable, Sendable {
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

public struct VideoCursor: Codable, Equatable, Sendable {
    public let score: Double
    public let size: Int64
    public let id: Int64

    public init(score: Double, size: Int64, id: Int64) {
        self.score = score
        self.size = size
        self.id = id
    }
}

public struct RandomCandidateResponse: Decodable, Equatable, Sendable {
    public let video: VideoSummary?
    public let reasonCode: String?
    public let userMessage: String?
}

public struct VideoListResponse: Decodable, Equatable, Sendable {
    public let videos: [VideoSummary]
    public let nextCursor: VideoCursor?
}

public struct ScannedFileResponse: Decodable, Equatable, Sendable {
    public let path: String
    public let size: Int64
}

public struct ScanDirectoryResponse: Decodable, Equatable, Sendable {
    public let files: [ScannedFileResponse]
}

public enum SubtitleEngine: String, Codable, Equatable, Sendable {
    case whisperx
    case qwen
}

public struct SubtitlePrepareRequest: Encodable, Equatable, Sendable {
    public let engine: SubtitleEngine

    public init(engine: SubtitleEngine) {
        self.engine = engine
    }
}

public struct SubtitleGenerateRequest: Encodable, Equatable, Sendable {
    public let videoId: Int64
    public let engine: SubtitleEngine
    public let sourceLang: String

    public init(videoId: Int64, engine: SubtitleEngine, sourceLang: String = "auto") {
        self.videoId = videoId
        self.engine = engine
        self.sourceLang = sourceLang
    }
}

public struct SubtitleEngineStatus: Decodable, Equatable, Sendable {
    public let engine: SubtitleEngine
    public let displayName: String
    public let supported: Bool
    public let available: Bool
    public let needsPrepare: Bool
    public let prepareMode: String
    public let reasonCode: String
    public let sourceLangMode: String
    public let reasonMessage: String
    public let prepareHint: String
}

public struct SubtitleGenerateResult: Decodable, Equatable, Sendable {
    public let status: String
    public let videoId: Int64
    public let path: String?
    public let message: String?
    public let validationCode: String?
    public let forceEligible: Bool
    public let engine: SubtitleEngine?
    public let sourceLang: String?
}

public struct SubtitleProgressRecord: Decodable, Equatable, Sendable {
    public let action: String
    public let engine: SubtitleEngine?
    public let phase: String
    public let percent: Int
    public let message: String
    public let cancellable: Bool
}

public struct SubtitleJobStatus: Decodable, Equatable, Sendable {
    public let running: Bool
    public let completed: Bool
    public let cancelled: Bool
    public let progress: SubtitleProgressRecord
    public let result: SubtitleGenerateResult?
    public let error: String?
}

public struct VideoMutationResponse: Decodable, Equatable, Sendable {
    public let video: VideoSummary?
    public let ok: Bool
    public let reasonCode: String?
    public let userMessage: String?
}

public struct BatchVideoOperationError: Decodable, Equatable, Sendable {
    public let videoId: Int64
    public let error: String
}

public struct BatchVideoOperationResult: Decodable, Equatable, Sendable {
    public let requested: Int
    public let succeeded: Int
    public let failed: Int
    public let errors: [BatchVideoOperationError]
}

public struct ScanSyncErrorRecord: Decodable, Equatable, Sendable {
    public let operation: String
    public let directory: String?
    public let path: String?
    public let error: String
}

public struct ScanSyncResponse: Decodable, Equatable, Sendable {
    public let directories: Int
    public let scanned: Int
    public let added: Int
    public let deleted: Int
    public let relocated: Int
    public let metadataRefreshed: Int
    public let skipped: Int
    public let errors: [ScanSyncErrorRecord]
}

public enum PreviewMode: String, Decodable, Equatable, Sendable {
    case inline
    case externalPreview = "external-preview"
    case unsupported
}

public struct PreviewSourceDescriptor: Decodable, Equatable, Sendable {
    public let locatorStrategy: String
    public let locatorValue: String
    public let mime: String
}

public struct PreviewExternalAction: Decodable, Equatable, Sendable {
    public let actionId: String
    public let buttonLabel: String
    public let hint: String
}

public struct PreviewSessionResponse: Decodable, Equatable, Sendable {
    public let videoId: Int64
    public let mode: PreviewMode
    public let displayName: String
    public let inlineSource: PreviewSourceDescriptor?
    public let externalAction: PreviewExternalAction?
    public let reasonCode: String?
    public let reasonMessage: String?
}

public struct PlaybackReconcileResult: Decodable, Equatable, Sendable {
    public let videoId: Int64
    public let didMarkStale: Bool
    public let didRelocate: Bool
    public let didRefreshMetadata: Bool
    public let needsReload: Bool
    public let updatedVideo: VideoSummary?
    public let reasonCode: String?
}

public struct PlaybackAttemptResponse: Decodable, Equatable, Sendable {
    public let video: VideoSummary?
    public let dispatchSucceeded: Bool
    public let userMessage: String?
    public let reasonCode: String?
    public let reconcileResult: PlaybackReconcileResult?
}

public struct TagRecord: Decodable, Equatable, Identifiable, Sendable {
    public let id: Int64
    public let name: String
    public let color: String

    public init(id: Int64, name: String, color: String) {
        self.id = id
        self.name = name
        self.color = color
    }
}

public struct TagListResponse: Decodable, Equatable, Sendable {
    public let tags: [TagRecord]
}

public struct ScanDirectoryRecord: Decodable, Equatable, Identifiable, Sendable {
    public let id: Int64
    public let path: String
    public let alias: String

    public init(id: Int64, path: String, alias: String) {
        self.id = id
        self.path = path
        self.alias = alias
    }
}

public struct ScanDirectoryListResponse: Decodable, Equatable, Sendable {
    public let directories: [ScanDirectoryRecord]
}

public struct PublicSettings: Decodable, Equatable, Sendable {
    public let confirmBeforeDelete: Bool
    public let deleteOriginalFile: Bool
    public let videoExtensions: String
    public let playWeight: Double
    public let autoScanOnStartup: Bool
    public let shortFeedMaxDurationMinutes: Int
    public let theme: String
    public let logEnabled: Bool
    public let bilingualEnabled: Bool
    public let bilingualLang: String
    public let deeplApiKeyConfigured: Bool
    public let aiTaggingBaseUrl: String
    public let aiTaggingApiKeyConfigured: Bool
    public let aiTaggingModel: String
    public let aiTaggingFrameCount: Int
    public let aiTaggingSubtitleCharLimit: Int
    public let aiTaggingStartupBatchSize: Int

    public init(
        confirmBeforeDelete: Bool,
        deleteOriginalFile: Bool,
        videoExtensions: String,
        playWeight: Double,
        autoScanOnStartup: Bool,
        shortFeedMaxDurationMinutes: Int,
        theme: String,
        logEnabled: Bool,
        bilingualEnabled: Bool,
        bilingualLang: String,
        deeplApiKeyConfigured: Bool,
        aiTaggingBaseUrl: String,
        aiTaggingApiKeyConfigured: Bool,
        aiTaggingModel: String,
        aiTaggingFrameCount: Int,
        aiTaggingSubtitleCharLimit: Int,
        aiTaggingStartupBatchSize: Int
    ) {
        self.confirmBeforeDelete = confirmBeforeDelete
        self.deleteOriginalFile = deleteOriginalFile
        self.videoExtensions = videoExtensions
        self.playWeight = playWeight
        self.autoScanOnStartup = autoScanOnStartup
        self.shortFeedMaxDurationMinutes = shortFeedMaxDurationMinutes
        self.theme = theme
        self.logEnabled = logEnabled
        self.bilingualEnabled = bilingualEnabled
        self.bilingualLang = bilingualLang
        self.deeplApiKeyConfigured = deeplApiKeyConfigured
        self.aiTaggingBaseUrl = aiTaggingBaseUrl
        self.aiTaggingApiKeyConfigured = aiTaggingApiKeyConfigured
        self.aiTaggingModel = aiTaggingModel
        self.aiTaggingFrameCount = aiTaggingFrameCount
        self.aiTaggingSubtitleCharLimit = aiTaggingSubtitleCharLimit
        self.aiTaggingStartupBatchSize = aiTaggingStartupBatchSize
    }
}

public struct SubtitleSegmentRecord: Decodable, Equatable, Sendable {
    public let index: Int
    public let startTimeMs: Int64
    public let endTimeMs: Int64
    public let text: String
    public let lines: [String]
}

public struct SubtitleSearchMatch: Decodable, Equatable, Sendable {
    public let video: VideoSummary
    public let segment: SubtitleSegmentRecord
}

public struct SubtitleSearchResponse: Decodable, Equatable, Sendable {
    public let matches: [SubtitleSearchMatch]
}

public enum AITagCandidateStatus: String, Decodable, Equatable, Sendable {
    case pending
    case approved
    case rejected
    case superseded
}

public struct AITagCandidateRecord: Decodable, Equatable, Identifiable, Sendable {
    public let id: Int64
    public let videoId: Int64
    public let videoName: String?
    public let videoPath: String?
    public let suggestedName: String
    public let normalizedName: String
    public let matchedTagId: Int64?
    public let confidence: String
    public let reasoning: String
    public let sourceSummary: String
    public let status: AITagCandidateStatus

    private enum CodingKeys: String, CodingKey {
        case id
        case videoId
        case videoName
        case videoPath
        case suggestedName
        case normalizedName
        case matchedTagId
        case confidence
        case reasoning
        case sourceSummary
        case status
        case video
    }

    private struct EmbeddedVideo: Decodable {
        let name: String?
        let path: String?
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(Int64.self, forKey: .id)
        videoId = try container.decode(Int64.self, forKey: .videoId)
        let embeddedVideo = try container.decodeIfPresent(EmbeddedVideo.self, forKey: .video)
        videoName = try container.decodeIfPresent(String.self, forKey: .videoName) ?? embeddedVideo?.name
        videoPath = try container.decodeIfPresent(String.self, forKey: .videoPath) ?? embeddedVideo?.path
        suggestedName = try container.decode(String.self, forKey: .suggestedName)
        normalizedName = try container.decode(String.self, forKey: .normalizedName)
        matchedTagId = try container.decodeIfPresent(Int64.self, forKey: .matchedTagId)
        confidence = try container.decode(String.self, forKey: .confidence)
        reasoning = try container.decode(String.self, forKey: .reasoning)
        sourceSummary = try container.decode(String.self, forKey: .sourceSummary)
        status = try container.decode(AITagCandidateStatus.self, forKey: .status)
    }
}

public struct AITagCandidateListResponse: Decodable, Equatable, Sendable {
    public let candidates: [AITagCandidateRecord]
}

public struct RejectAITagCandidatesByVideoResponse: Decodable, Equatable, Sendable {
    public let rejected: Int64
}

public struct AITaggingStatusSummary: Decodable, Equatable, Sendable {
    public let configAvailable: Bool
    public let pending: Int64
    public let processing: Int64
    public let completed: Int64
    public let skipped: Int64
    public let failed: Int64
}

public struct AITagCandidateGroup: Equatable, Identifiable, Sendable {
    public let videoId: Int64
    public let videoName: String
    public let videoPath: String
    public let candidates: [AITagCandidateRecord]

    public var id: Int64 { videoId }
    public var pendingCount: Int { candidates.filter { $0.status == .pending }.count }
}

public struct ShortFeedInteractionRecord: Decodable, Equatable, Sendable {
    public let videoId: Int64
    public let liked: Bool
    public let favorited: Bool
    public let viewCount: Int
}

public struct ShortFeedServerStatus: Decodable, Equatable, Sendable {
    public let running: Bool
    public let bindAddress: String
    public let port: Int
    public let url: String
    public let lanUrls: [String]
    public let startupError: String
    public let fallbackUsed: Bool
    public let allowedAccess: String
}

public struct ShortFeedVideoRecord: Decodable, Equatable, Identifiable, Sendable {
    public let id: Int64
    public let name: String
    public let duration: Double
    public let width: Int
    public let height: Int
    public let tags: [VideoTagSummary]
    public let mediaUrl: String
    public let mediaMime: String
    public let liked: Bool
    public let favorited: Bool
    public let reasonCode: String
    public let reasonMessage: String
}

public struct CleanupDuplicateGroup: Decodable, Equatable, Sendable {
    public let originalId: Int64
    public let candidateIds: [Int64]
    public let reason: String
}

public struct CleanupAnalysisRecord: Decodable, Equatable, Sendable {
    public let duplicateGroups: [CleanupDuplicateGroup]
    public let lowDurationIds: [Int64]
    public let lowResolutionIds: [Int64]

    public var allCandidateIds: [Int64] {
        var ids = Set<Int64>()
        for group in duplicateGroups {
            ids.insert(group.originalId)
            for candidateId in group.candidateIds {
                ids.insert(candidateId)
            }
        }
        for id in lowDurationIds {
            ids.insert(id)
        }
        for id in lowResolutionIds {
            ids.insert(id)
        }
        return ids.sorted()
    }
}

public struct CleanupProgressRecord: Decodable, Equatable, Sendable {
    public let stage: String
    public let message: String
    public let current: Int
    public let total: Int
    public let path: String
}

public struct CleanupStatus: Decodable, Equatable, Sendable {
    public let running: Bool
    public let completed: Bool
    public let error: String
    public let progress: CleanupProgressRecord
    public let analysis: CleanupAnalysisRecord?
    public let startedAt: String?
    public let updatedAt: String?
}

public struct DiagnosticsSnapshot: Decodable, Equatable, Sendable {
    public let videoCount: Int64
    public let tagCount: Int64
    public let subtitleSegmentCount: Int64
    public let aiCandidateCount: Int64
    public let shortFeedInteractionCount: Int64
    public let redactedSettings: PublicSettings
}

public extension Array where Element == AITagCandidateRecord {
    func groupedByVideo() -> [AITagCandidateGroup] {
        let grouped = Dictionary(grouping: self) { $0.videoId }
        return grouped.keys.sorted().map { videoId in
            let candidates = grouped[videoId] ?? []
            let first = candidates.first
            return AITagCandidateGroup(
                videoId: videoId,
                videoName: first?.videoName ?? "Video #\(videoId)",
                videoPath: first?.videoPath ?? "",
                candidates: candidates.sorted { left, right in
                    if left.status != right.status {
                        return left.status.sortRank < right.status.sortRank
                    }
                    return left.id < right.id
                }
            )
        }
    }
}

private extension AITagCandidateStatus {
    var sortRank: Int {
        switch self {
        case .pending: return 0
        case .approved: return 1
        case .rejected: return 2
        case .superseded: return 3
        }
    }
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
