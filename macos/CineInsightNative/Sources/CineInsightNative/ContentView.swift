import AppKit
import AVKit
import CineInsightNativeCore
import SwiftUI

struct ContentView: View {
    @StateObject private var daemon: DaemonLifecycleManager
    @StateObject private var library: LibraryViewModel
    @State private var selection: SidebarSection? = .library
    @State private var renameText = ""
    @State private var newTagName = ""
    @State private var tagName = ""
    @State private var tagColor = ""
    @State private var editingTag: TagRecord?
    @State private var directoryPath = ""
    @State private var directoryAlias = ""
    @State private var editingDirectory: ScanDirectoryRecord?
    @State private var videoPath = ""
    @State private var deleteFile = false

    private let client: NativeAPIClient

    init() {
        let configuration = Self.defaultConfiguration()
        let client = NativeAPIClient(configuration: configuration)
        self.client = client
        _daemon = StateObject(wrappedValue: DaemonLifecycleManager())
        _library = StateObject(wrappedValue: LibraryViewModel(client: client))
    }

    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                Section("Workspace") {
                    sidebarItem(.library, "Library", "film.stack")
                    sidebarItem(.tags, "Tags", "tag")
                    sidebarItem(.directories, "Directories", "folder")
                    sidebarItem(.subtitles, "Subtitles", "captions.bubble")
                    sidebarItem(.aiTags, "AI Tags", "sparkles")
                    sidebarItem(.shortFeed, "Short Feed", "rectangle.portrait")
                    sidebarItem(.cleanup, "Cleanup", "trash")
                    sidebarItem(.diagnostics, "Diagnostics", "waveform.path.ecg")
                }
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 220)
        } content: {
            contentColumn
        } detail: {
            detailColumn
        }
        .frame(minWidth: 1120, minHeight: 720)
        .task {
            daemon.launch(client.configuration)
            await daemon.refreshHealth(using: client)
            await library.loadAll()
        }
        .onChange(of: library.selectedVideoID) {
            renameText = library.selectedVideo?.nameWithoutExtension ?? ""
            Task { await library.refreshPreview() }
        }
        .onChange(of: library.query) {
            Task { await library.search() }
        }
    }

    private var contentColumn: some View {
        VStack(spacing: 0) {
            toolbar
            Divider()
            switch selection ?? .library {
            case .library:
                videoTable
            case .tags:
                tagsPanel
            case .directories:
                directoriesPanel
            case .subtitles:
                subtitlesPanel
            case .aiTags:
                aiTagsPanel
            case .shortFeed:
                shortFeedPanel
            case .cleanup:
                cleanupPanel
            case .diagnostics:
                diagnosticsPanel
            }
        }
    }

    private var detailColumn: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                daemonBanner
                selectedVideoPanel
                previewPanel
                operationsPanel
                settingsPanel
            }
            .padding(18)
        }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            TextField("Search videos, paths, or tags", text: $library.query)
                .textFieldStyle(.roundedBorder)
                .frame(minWidth: 280)

            Button {
                Task { await library.loadAll() }
            } label: {
                Label("Refresh", systemImage: "arrow.clockwise")
            }

            Button {
                chooseVideoFile()
            } label: {
                Label("Add", systemImage: "plus")
            }

            Spacer()

            Button {
                Task { await library.previewExternally() }
            } label: {
                Label("Preview", systemImage: "play.rectangle")
            }
            .disabled(library.selectedVideo == nil)

            Button {
                Task { await library.playSelected() }
            } label: {
                Label("Play", systemImage: "play.fill")
            }
            .disabled(library.selectedVideo == nil)

            Button {
                Task { await library.playRandom() }
            } label: {
                Label("Random", systemImage: "shuffle")
            }
        }
        .padding(12)
    }

    private var videoTable: some View {
        Table(library.filteredVideos, selection: $library.selectedVideoID) {
            TableColumn("Name") { video in
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(video.name)
                            .font(.body)
                        if video.isStale {
                            Image(systemName: "exclamationmark.triangle.fill")
                                .foregroundStyle(.orange)
                        }
                    }
                    Text(video.directory)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            TableColumn("Tags") { video in
                FlowTags(tags: video.tags)
            }
            TableColumn("Resolution") { video in
                Text(video.resolution.isEmpty ? "-" : video.resolution)
            }
            TableColumn("Duration") { video in
                Text(formatDuration(video.duration))
                    .monospacedDigit()
            }
            TableColumn("Score") { video in
                Text(video.score, format: .number.precision(.fractionLength(1)))
                    .monospacedDigit()
            }
        }
        .overlay {
            if library.isLoading && library.videos.isEmpty {
                ProgressView("Loading library")
            } else if library.filteredVideos.isEmpty {
                ContentUnavailableView("No Videos", systemImage: "film")
            }
        }
    }

    private var selectedVideoPanel: some View {
        Group {
            if let video = library.selectedVideo {
                VStack(alignment: .leading, spacing: 10) {
                    HStack(alignment: .firstTextBaseline) {
                        Text(video.name)
                            .font(.title3)
                        Spacer()
                        Text(video.score, format: .number.precision(.fractionLength(1)))
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                    }
                    Text(video.path)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                    Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                        metricRow("Duration", formatDuration(video.duration))
                        metricRow("Size", ByteCountFormatter.string(fromByteCount: video.size, countStyle: .file))
                        metricRow("Resolution", video.resolution.isEmpty ? "-" : video.resolution)
                        metricRow("Plays", "\(video.playCount) formal / \(video.randomPlayCount) random")
                    }
                    .font(.callout)
                    FlowTags(tags: video.tags)
                }
            } else {
                ContentUnavailableView("No Selection", systemImage: "film")
            }
        }
    }

    private var previewPanel: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Preview")
                .font(.headline)
            if let preview = library.preview {
                switch preview.mode {
                case .inline:
                    if let locator = preview.inlineSource?.locatorValue, let url = client.absoluteURL(for: locator) {
                        VideoPlayer(player: AVPlayer(url: url))
                            .aspectRatio(16 / 9, contentMode: .fit)
                            .clipShape(RoundedRectangle(cornerRadius: 8))
                    } else {
                        previewPlaceholder("Inline preview source is unavailable")
                    }
                case .externalPreview:
                    previewPlaceholder(preview.reasonMessage ?? "Use external preview for this file")
                case .unsupported:
                    previewPlaceholder(preview.reasonMessage ?? "Preview is not supported")
                }
            } else {
                previewPlaceholder("Select a video to load preview metadata")
            }
        }
    }

    private var operationsPanel: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Operations")
                .font(.headline)
            HStack(spacing: 8) {
                TextField("Video path", text: $videoPath)
                    .textFieldStyle(.roundedBorder)
                Button {
                    chooseVideoFile()
                } label: {
                    Image(systemName: "folder")
                }
                Button {
                    let path = videoPath
                    videoPath = ""
                    Task { await library.addVideo(path: path) }
                } label: {
                    Label("Add Video", systemImage: "plus")
                }
            }
            HStack(spacing: 8) {
                TextField("New filename", text: $renameText)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task { await library.renameSelected(to: renameText) }
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                .disabled(library.selectedVideo == nil)
            }
            Toggle("Delete original file", isOn: $deleteFile)
            Button(role: .destructive) {
                Task { await library.deleteSelected(deleteFile: deleteFile) }
            } label: {
                Label("Delete Video", systemImage: "trash")
            }
            .disabled(library.selectedVideo == nil)

            Divider()

            LazyVGrid(columns: [GridItem(.adaptive(minimum: 132), spacing: 8)], alignment: .leading, spacing: 8) {
                ForEach(library.tags) { tag in
                    Toggle(isOn: tagBinding(tag)) {
                        Label(tag.name, systemImage: "tag")
                    }
                    .toggleStyle(.button)
                }
            }
            HStack(spacing: 8) {
                TextField("Create tag", text: $newTagName)
                    .textFieldStyle(.roundedBorder)
                Button {
                    let name = newTagName
                    newTagName = ""
                    Task { await library.createAndAssignTag(name: name) }
                } label: {
                    Label("Add Tag", systemImage: "plus")
                }
                .disabled(library.selectedVideo == nil)
            }
        }
    }

    private var tagsPanel: some View {
        VStack(spacing: 0) {
            List(library.tags) { tag in
                HStack(spacing: 10) {
                    Circle()
                        .fill(Color(hex: tag.color) ?? .accentColor)
                        .frame(width: 10, height: 10)
                    Text(tag.name)
                    Spacer()
                    Text(tag.color)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Button {
                        editingTag = tag
                        tagName = tag.name
                        tagColor = tag.color
                    } label: {
                        Image(systemName: "pencil")
                    }
                    Button(role: .destructive) {
                        Task { await library.deleteTag(tag) }
                    } label: {
                        Image(systemName: "trash")
                    }
                }
            }
            .overlay {
                if library.tags.isEmpty {
                    ContentUnavailableView("No Tags", systemImage: "tag")
                }
            }
            Divider()
            HStack(spacing: 8) {
                TextField("Tag name", text: $tagName)
                    .textFieldStyle(.roundedBorder)
                TextField("Color", text: $tagColor)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 110)
                Button {
                    Task {
                        if let editingTag {
                            await library.updateTag(editingTag, name: tagName, color: tagColor)
                        } else {
                            await library.createTag(name: tagName, color: tagColor)
                        }
                        editingTag = nil
                        tagName = ""
                        tagColor = ""
                    }
                } label: {
                    Label(editingTag == nil ? "Add" : "Update", systemImage: editingTag == nil ? "plus" : "checkmark")
                }
                Button {
                    editingTag = nil
                    tagName = ""
                    tagColor = ""
                } label: {
                    Label("Cancel", systemImage: "xmark")
                }
                .disabled(editingTag == nil && tagName.isEmpty && tagColor.isEmpty)
            }
            .padding(12)
        }
    }

    private var directoriesPanel: some View {
        VStack(spacing: 0) {
            List(library.directories) { directory in
                HStack(spacing: 12) {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(directory.alias.isEmpty ? directory.path : directory.alias)
                        Text(directory.path)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                    Spacer()
                    Button {
                        editingDirectory = directory
                        directoryPath = directory.path
                        directoryAlias = directory.alias
                    } label: {
                        Image(systemName: "pencil")
                    }
                    Button {
                        Task { await library.scanDirectory(directory) }
                    } label: {
                        Image(systemName: "magnifyingglass")
                    }
                    Button(role: .destructive) {
                        Task { await library.deleteDirectory(directory) }
                    } label: {
                        Image(systemName: "trash")
                    }
                }
            }
            Divider()
            HStack(spacing: 8) {
                TextField("Path", text: $directoryPath)
                    .textFieldStyle(.roundedBorder)
                Button {
                    chooseDirectory()
                } label: {
                    Image(systemName: "folder")
                }
                TextField("Alias", text: $directoryAlias)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task {
                        if let editingDirectory {
                            await library.updateDirectory(editingDirectory, path: directoryPath, alias: directoryAlias)
                        } else {
                            await library.addDirectory(path: directoryPath, alias: directoryAlias)
                        }
                        editingDirectory = nil
                        directoryPath = ""
                        directoryAlias = ""
                    }
                } label: {
                    Label(editingDirectory == nil ? "Add" : "Update", systemImage: editingDirectory == nil ? "plus" : "checkmark")
                }
                Button {
                    editingDirectory = nil
                    directoryPath = ""
                    directoryAlias = ""
                } label: {
                    Label("Cancel", systemImage: "xmark")
                }
                .disabled(editingDirectory == nil && directoryPath.isEmpty && directoryAlias.isEmpty)
            }
            .padding(12)
        }
    }

    private var subtitlesPanel: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                TextField("Search subtitle text", text: $library.subtitleQuery)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task { await library.searchSubtitles() }
                } label: {
                    Label("Search", systemImage: "magnifyingglass")
                }
            }
            .padding(12)
            List(library.subtitleMatches, id: \.segment.index) { match in
                VStack(alignment: .leading, spacing: 4) {
                    Text(match.video.name)
                        .font(.headline)
                    Text(match.segment.text)
                    Text("\(match.segment.startTimeMs)ms - \(match.segment.endTimeMs)ms")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private var aiTagsPanel: some View {
        List(library.aiCandidates) { candidate in
            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text(candidate.suggestedName)
                        .font(.headline)
                    Spacer()
                    Text(candidate.status.rawValue)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Text(candidate.reasoning)
                    .font(.callout)
                Text(candidate.sourceSummary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
                HStack {
                    Button {
                        Task { await library.approveCandidate(candidate) }
                    } label: {
                        Label("Approve", systemImage: "checkmark")
                    }
                    .disabled(candidate.status != .pending)

                    Button(role: .destructive) {
                        Task { await library.rejectCandidate(candidate) }
                    } label: {
                        Label("Reject", systemImage: "xmark")
                    }
                    .disabled(candidate.status != .pending)
                }
            }
        }
        .overlay {
            if library.aiCandidates.isEmpty {
                ContentUnavailableView("No AI Tag Candidates", systemImage: "sparkles")
            }
        }
    }

    private var shortFeedPanel: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Button {
                    Task { await library.loadShortFeedVideo() }
                } label: {
                    Label("Next", systemImage: "forward.fill")
                }
                Button {
                    Task { await library.recordShortFeed(viewed: true) }
                } label: {
                    Label("Viewed", systemImage: "eye")
                }
                Button {
                    Task { await library.recordShortFeed(liked: true) }
                } label: {
                    Label("Like", systemImage: "hand.thumbsup")
                }
                Button {
                    Task { await library.recordShortFeed(favorited: true) }
                } label: {
                    Label("Favorite", systemImage: "star")
                }
            }

            if let video = library.shortFeedVideo {
                VStack(alignment: .leading, spacing: 8) {
                    Text(video.name)
                        .font(.title3)
                    Text("\(formatDuration(video.duration)) · \(video.width)x\(video.height)")
                        .foregroundStyle(.secondary)
                    FlowTags(tags: video.tags)
                }
            } else {
                ContentUnavailableView("No Short Feed Video", systemImage: "rectangle.portrait")
            }
        }
        .padding(12)
    }

    private var cleanupPanel: some View {
        VStack(alignment: .leading, spacing: 12) {
            Button {
                Task { await library.analyzeCleanup() }
            } label: {
                Label("Analyze", systemImage: "wand.and.stars")
            }
            if let cleanup = library.cleanup {
                Text("Duplicate groups: \(cleanup.duplicateGroups.count)")
                Text("Short videos: \(cleanup.lowDurationIds.count)")
                Text("Low resolution: \(cleanup.lowResolutionIds.count)")
                List(cleanup.duplicateGroups, id: \.originalId) { group in
                    Text("\(group.reason): \(group.originalId) -> \(group.candidateIds.map(String.init).joined(separator: ", "))")
                }
            } else {
                ContentUnavailableView("No Cleanup Analysis", systemImage: "trash")
            }
        }
        .padding(12)
    }

    private var diagnosticsPanel: some View {
        VStack(alignment: .leading, spacing: 12) {
            Button {
                Task { await library.refreshDiagnostics() }
            } label: {
                Label("Refresh Diagnostics", systemImage: "arrow.clockwise")
            }
            if let diagnostics = library.diagnostics {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                    metricRow("Videos", "\(diagnostics.videoCount)")
                    metricRow("Tags", "\(diagnostics.tagCount)")
                    metricRow("Subtitles", "\(diagnostics.subtitleSegmentCount)")
                    metricRow("AI Candidates", "\(diagnostics.aiCandidateCount)")
                    metricRow("Short Feed", "\(diagnostics.shortFeedInteractionCount)")
                }
            } else {
                ContentUnavailableView("No Diagnostics", systemImage: "waveform.path.ecg")
            }
        }
        .padding(12)
    }

    private var settingsPanel: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Settings")
                .font(.headline)
            if let settings = library.settings {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                    metricRow("Extensions", settings.videoExtensions)
                    metricRow("Play Weight", String(format: "%.1f", settings.playWeight))
                    metricRow("Theme", settings.theme)
                    metricRow("DeepL", settings.deeplApiKeyConfigured ? "Configured" : "Not configured")
                    metricRow("AI Tagging", settings.aiTaggingApiKeyConfigured ? "Configured" : "Not configured")
                }
                .font(.callout)
            } else {
                Text("Settings unavailable")
                    .foregroundStyle(.secondary)
            }
            Text(library.statusMessage)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(3)
        }
    }

    private var daemonBanner: some View {
        HStack(spacing: 10) {
            Circle()
                .fill(daemon.state == .running ? .green : daemon.state == .failed ? .red : .orange)
                .frame(width: 9, height: 9)
            Text(daemon.message)
                .font(.caption)
            Spacer()
            if let health = daemon.health {
                Text("\(health.service) \(health.version)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func sidebarItem(_ section: SidebarSection, _ title: String, _ icon: String) -> some View {
        Label(title, systemImage: icon)
            .tag(section)
    }

    private func metricRow(_ label: String, _ value: String) -> some View {
        GridRow {
            Text(label).foregroundStyle(.secondary)
            Text(value)
        }
    }

    private func previewPlaceholder(_ text: String) -> some View {
        ZStack {
            Rectangle()
                .fill(.quaternary)
            VStack(spacing: 8) {
                Image(systemName: "play.rectangle")
                    .font(.system(size: 42))
                    .foregroundStyle(.secondary)
                Text(text)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .aspectRatio(16 / 9, contentMode: .fit)
        .clipShape(RoundedRectangle(cornerRadius: 8))
    }

    private func tagBinding(_ tag: TagRecord) -> Binding<Bool> {
        Binding {
            library.selectedVideo?.tags.contains { $0.id == tag.id } ?? false
        } set: { enabled in
            Task { await library.setTag(tag, enabled: enabled) }
        }
    }

    private func chooseVideoFile() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let path = panel.url?.path {
            videoPath = path
            Task { await library.addVideo(path: path) }
        }
    }

    private func chooseDirectory() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let path = panel.url?.path {
            directoryPath = path
        }
    }

    private func formatDuration(_ seconds: Double) -> String {
        guard seconds.isFinite, seconds > 0 else { return "-" }
        let total = Int(seconds.rounded())
        return String(format: "%02d:%02d", total / 60, total % 60)
    }

    private static func defaultConfiguration() -> DaemonLaunchConfiguration {
        let port = Int(ProcessInfo.processInfo.environment["CINE_DAEMON_PORT"] ?? "") ?? 18088
        let token = ProcessInfo.processInfo.environment["CINE_DAEMON_TOKEN"] ?? "dev-token"
        let executable = ProcessInfo.processInfo.environment["CINE_DAEMON_PATH"] ?? "cine-daemon"
        return DaemonLaunchConfiguration(executablePath: executable, port: port, token: token)
    }
}

private enum SidebarSection: Hashable {
    case library
    case tags
    case directories
    case subtitles
    case aiTags
    case shortFeed
    case cleanup
    case diagnostics
}

private struct FlowTags: View {
    let tags: [VideoTagSummary]

    var body: some View {
        HStack {
            ForEach(tags, id: \.id) { tag in
                Text(tag.name)
                    .font(.caption)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background((Color(hex: tag.color) ?? .accentColor).opacity(0.16))
                    .foregroundStyle(.primary)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
        }
    }
}

private extension VideoSummary {
    var nameWithoutExtension: String {
        NSString(string: name).deletingPathExtension
    }
}

private extension Color {
    init?(hex: String) {
        let trimmed = hex.trimmingCharacters(in: CharacterSet(charactersIn: "#"))
        guard trimmed.count == 6, let value = Int(trimmed, radix: 16) else {
            return nil
        }
        let red = Double((value >> 16) & 0xff) / 255.0
        let green = Double((value >> 8) & 0xff) / 255.0
        let blue = Double(value & 0xff) / 255.0
        self.init(red: red, green: green, blue: blue)
    }
}
