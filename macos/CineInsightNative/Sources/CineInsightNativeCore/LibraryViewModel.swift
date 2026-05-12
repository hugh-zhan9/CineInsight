import Combine
import Foundation

@MainActor
public final class LibraryViewModel: ObservableObject {
    @Published public private(set) var videos: [VideoSummary]
    @Published public private(set) var tags: [TagRecord]
    @Published public private(set) var directories: [ScanDirectoryRecord]
    @Published public private(set) var settings: PublicSettings?
    @Published public private(set) var preview: PreviewSessionResponse?
    @Published public private(set) var subtitleMatches: [SubtitleSearchMatch]
    @Published public private(set) var aiCandidates: [AITagCandidateRecord]
    @Published public private(set) var cleanup: CleanupAnalysisRecord?
    @Published public private(set) var diagnostics: DiagnosticsSnapshot?
    @Published public private(set) var shortFeedVideo: ShortFeedVideoRecord?
    @Published public private(set) var isLoading: Bool
    @Published public private(set) var statusMessage: String
    @Published public var selectedVideoID: Int64?
    @Published public var query: String
    @Published public var subtitleQuery: String

    private let client: NativeAPIClient

    public init(
        client: NativeAPIClient,
        videos: [VideoSummary] = [],
        tags: [TagRecord] = [],
        directories: [ScanDirectoryRecord] = [],
        settings: PublicSettings? = nil
    ) {
        self.client = client
        self.videos = videos
        self.tags = tags
        self.directories = directories
        self.settings = settings
        self.preview = nil
        self.subtitleMatches = []
        self.aiCandidates = []
        self.cleanup = nil
        self.diagnostics = nil
        self.shortFeedVideo = nil
        self.isLoading = false
        self.statusMessage = "Ready"
        self.selectedVideoID = videos.first?.id
        self.query = ""
        self.subtitleQuery = ""
    }

    public var selectedVideo: VideoSummary? {
        videos.first { $0.id == selectedVideoID }
    }

    public var filteredVideos: [VideoSummary] {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return videos }
        return videos.filter { video in
            video.name.localizedCaseInsensitiveContains(trimmed)
                || video.path.localizedCaseInsensitiveContains(trimmed)
                || video.tags.contains { $0.name.localizedCaseInsensitiveContains(trimmed) }
        }
    }

    public func loadAll() async {
        await run("Loading library") {
            async let videosPage = client.listVideos()
            async let tagList = client.listTags()
            async let directoryList = client.listScanDirectories()
            async let publicSettings = client.settings()
            async let candidates = client.listAITagCandidates()
            async let diag = client.diagnostics()

            let loadedVideosPage = try await videosPage
            let loadedTagList = try await tagList
            let loadedDirectoryList = try await directoryList
            let loadedSettings = try await publicSettings
            let loadedCandidates = try await candidates
            let loadedDiagnostics = try await diag

            videos = loadedVideosPage.videos
            tags = loadedTagList.tags
            directories = loadedDirectoryList.directories
            settings = loadedSettings
            aiCandidates = loadedCandidates.candidates
            diagnostics = loadedDiagnostics
            if selectedVideoID == nil || !videos.contains(where: { $0.id == selectedVideoID }) {
                selectedVideoID = videos.first?.id
            }
            statusMessage = "Library loaded"
        }
    }

    public func search() async {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        await run(trimmed.isEmpty ? "Loading videos" : "Searching videos") {
            let page: VideoListResponse
            if trimmed.isEmpty {
                page = try await client.listVideos()
            } else {
                page = try await client.searchVideos(VideoFilterRequest(keyword: trimmed, limit: 80))
            }
            videos = page.videos
            selectedVideoID = videos.first?.id
            statusMessage = trimmed.isEmpty ? "Videos loaded" : "Search updated"
        }
    }

    public func refreshPreview() async {
        guard let video = selectedVideo else { return }
        await run("Loading preview") {
            preview = try await client.previewSession(videoId: video.id)
            statusMessage = "Preview ready"
        }
    }

    public func previewExternally() async {
        guard let video = selectedVideo else { return }
        await run("Opening preview") {
            try await client.previewExternally(videoId: video.id)
            statusMessage = "External preview dispatched"
        }
    }

    public func playSelected() async {
        guard let video = selectedVideo else { return }
        await run("Playing video") {
            let result = try await client.playVideo(id: video.id)
            applyPlayback(result)
            statusMessage = result.userMessage ?? (result.dispatchSucceeded ? "Playback dispatched" : "Playback failed")
        }
    }

    public func playRandom() async {
        await run("Playing random video") {
            let result = try await client.playRandomVideo()
            applyPlayback(result)
            if let id = result.video?.id {
                selectedVideoID = id
            }
            statusMessage = result.userMessage ?? (result.dispatchSucceeded ? "Random playback dispatched" : "Random playback failed")
        }
    }

    public func renameSelected(to name: String) async {
        guard let video = selectedVideo else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            statusMessage = "Name cannot be empty"
            return
        }
        await run("Renaming video") {
            let response = try await client.renameVideo(id: video.id, name: trimmed)
            applyMutation(response)
            statusMessage = response.userMessage ?? "Video renamed"
        }
    }

    public func deleteSelected(deleteFile: Bool = false) async {
        guard let video = selectedVideo else { return }
        await run("Deleting video") {
            let response = try await client.deleteVideo(id: video.id, deleteFile: deleteFile)
            videos.removeAll { $0.id == video.id }
            if selectedVideoID == video.id {
                selectedVideoID = videos.first?.id
            }
            statusMessage = response.userMessage ?? "Video deleted"
        }
    }

    public func setTag(_ tag: TagRecord, enabled: Bool) async {
        guard let video = selectedVideo else { return }
        await run(enabled ? "Assigning tag" : "Removing tag") {
            if enabled {
                try await client.assignTag(videoId: video.id, tagId: tag.id)
            } else {
                try await client.removeTag(videoId: video.id, tagId: tag.id)
            }
            await search()
            selectedVideoID = video.id
            statusMessage = enabled ? "Tag assigned" : "Tag removed"
        }
    }

    public func createAndAssignTag(name: String) async {
        guard let video = selectedVideo else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        await run("Creating tag") {
            let tag = try await client.createTag(name: trimmed)
            let tagList = try await client.listTags()
            tags = tagList.tags
            try await client.assignTag(videoId: video.id, tagId: tag.id)
            await search()
            selectedVideoID = video.id
            statusMessage = "Tag created and assigned"
        }
    }

    public func createTag(name: String, color: String = "") async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            statusMessage = "Tag name cannot be empty"
            return
        }
        await run("Creating tag") {
            _ = try await client.createTag(name: trimmed, color: color)
            let tagList = try await client.listTags()
            tags = tagList.tags
            statusMessage = "Tag created"
        }
    }

    public func updateTag(_ tag: TagRecord, name: String, color: String) async {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            statusMessage = "Tag name cannot be empty"
            return
        }
        await run("Updating tag") {
            let updated = try await client.updateTag(id: tag.id, name: trimmed, color: color)
            if let index = tags.firstIndex(where: { $0.id == tag.id }) {
                tags[index] = updated
            }
            await search()
            statusMessage = "Tag updated"
        }
    }

    public func deleteTag(_ tag: TagRecord) async {
        await run("Deleting tag") {
            try await client.deleteTag(id: tag.id)
            tags.removeAll { $0.id == tag.id }
            await search()
            statusMessage = "Tag deleted"
        }
    }

    public func addDirectory(path: String, alias: String) async {
        let trimmedPath = path.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedAlias = alias.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedPath.isEmpty else {
            statusMessage = "Directory path cannot be empty"
            return
        }
        await run("Adding directory") {
            _ = try await client.addScanDirectory(path: trimmedPath, alias: trimmedAlias)
            let response = try await client.listScanDirectories()
            directories = response.directories
            statusMessage = "Directory added"
        }
    }

    public func updateDirectory(_ directory: ScanDirectoryRecord, path: String, alias: String) async {
        let trimmedPath = path.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedAlias = alias.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedPath.isEmpty else {
            statusMessage = "Directory path cannot be empty"
            return
        }
        await run("Updating directory") {
            _ = try await client.updateScanDirectory(id: directory.id, path: trimmedPath, alias: trimmedAlias)
            let response = try await client.listScanDirectories()
            directories = response.directories
            statusMessage = "Directory updated"
        }
    }

    public func deleteDirectory(_ directory: ScanDirectoryRecord) async {
        await run("Deleting directory") {
            try await client.deleteScanDirectory(id: directory.id)
            directories.removeAll { $0.id == directory.id }
            statusMessage = "Directory deleted"
        }
    }

    public func scanDirectory(_ directory: ScanDirectoryRecord) async {
        await run("Scanning directory") {
            let response = try await client.scanDirectory(path: directory.path, extensions: settings?.videoExtensions)
            statusMessage = "Scan found \(response.files.count) files"
        }
    }

    public func addVideo(path: String) async {
        let trimmed = path.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            statusMessage = "Video path cannot be empty"
            return
        }
        await run("Adding video") {
            let response = try await client.addVideo(path: trimmed)
            applyMutation(response)
            statusMessage = response.userMessage ?? "Video added"
        }
    }

    public func approveCandidate(_ candidate: AITagCandidateRecord) async {
        await run("Approving AI tag") {
            let updated = try await client.approveAITagCandidate(id: candidate.id)
            replaceCandidate(updated)
            let tagList = try await client.listTags()
            tags = tagList.tags
            await search()
            statusMessage = "AI tag approved"
        }
    }

    public func rejectCandidate(_ candidate: AITagCandidateRecord) async {
        await run("Rejecting AI tag") {
            let updated = try await client.rejectAITagCandidate(id: candidate.id)
            replaceCandidate(updated)
            statusMessage = "AI tag rejected"
        }
    }

    public func loadShortFeedVideo() async {
        await run("Loading short feed") {
            shortFeedVideo = try await client.nextShortFeedVideo()
            statusMessage = "Short feed video loaded"
        }
    }

    public func recordShortFeed(viewed: Bool = false, liked: Bool? = nil, favorited: Bool? = nil) async {
        guard let video = shortFeedVideo else { return }
        await run("Saving short feed feedback") {
            _ = try await client.recordShortFeedFeedback(
                videoId: video.id,
                request: ShortFeedFeedbackRequest(liked: liked, favorited: favorited, viewed: viewed)
            )
            statusMessage = "Short feed feedback saved"
        }
    }

    public func searchSubtitles() async {
        let trimmed = subtitleQuery.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            subtitleMatches = []
            return
        }
        await run("Searching subtitles") {
            let response = try await client.searchSubtitles(keyword: trimmed)
            subtitleMatches = response.matches
            statusMessage = "Subtitle search updated"
        }
    }

    public func analyzeCleanup() async {
        await run("Analyzing cleanup") {
            cleanup = try await client.analyzeCleanup()
            statusMessage = "Cleanup analysis updated"
        }
    }

    public func refreshDiagnostics() async {
        await run("Loading diagnostics") {
            diagnostics = try await client.diagnostics()
            statusMessage = "Diagnostics updated"
        }
    }

    private func run(_ loadingMessage: String, operation: () async throws -> Void) async {
        isLoading = true
        statusMessage = loadingMessage
        do {
            try await operation()
        } catch {
            statusMessage = error.localizedDescription
        }
        isLoading = false
    }

    private func applyMutation(_ response: VideoMutationResponse) {
        guard let video = response.video else { return }
        if let index = videos.firstIndex(where: { $0.id == video.id }) {
            videos[index] = video
        } else {
            videos.insert(video, at: 0)
        }
        selectedVideoID = video.id
    }

    private func applyPlayback(_ response: PlaybackAttemptResponse) {
        if let video = response.video {
            replace(video)
        }
        if let updated = response.reconcileResult?.updatedVideo {
            replace(updated)
        }
        if response.reconcileResult?.needsReload == true {
            Task { await search() }
        }
    }

    private func replace(_ video: VideoSummary) {
        if let index = videos.firstIndex(where: { $0.id == video.id }) {
            videos[index] = video
        }
    }

    private func replaceCandidate(_ candidate: AITagCandidateRecord) {
        if let index = aiCandidates.firstIndex(where: { $0.id == candidate.id }) {
            aiCandidates[index] = candidate
        }
    }
}
