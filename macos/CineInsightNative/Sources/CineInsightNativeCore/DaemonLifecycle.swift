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

    public init(state: DaemonLifecycleState = .stopped, health: DaemonHealth? = nil) {
        self.state = state
        self.health = health
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

public struct VideoCursor: Decodable, Equatable {
    public let score: Double
    public let size: Int64
    public let id: Int64
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
