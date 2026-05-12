import CineInsightNativeCore
import SwiftUI

struct ContentView: View {
    @StateObject private var manager = DaemonLifecycleManager()
    @State private var query = ""
    @State private var selection: Int64? = 3
    @State private var videos: [NativeVideo] = [
        NativeVideo(
            id: 3,
            name: "clip.mp4",
            path: "/library/clip.mp4",
            directory: "/library",
            size: 100,
            duration: 12.5,
            resolution: "1920x1080",
            width: 1920,
            height: 1080,
            isStale: false,
            playCount: 1,
            randomPlayCount: 2,
            score: 4.0,
            tagIds: [9]
        )
    ]
    @State private var tags: [TagRecord] = [
        TagRecord(id: 9, name: "keep", color: "#5b8def"),
        TagRecord(id: 10, name: "review", color: "#0D9488")
    ]
    @State private var renameText = "clip"
    @State private var newTagName = ""
    @State private var statusMessage = "Ready"

    private let directories = [
        ScanDirectoryRecord(id: 1, path: "/library", alias: "Library")
    ]
    private let settings = PublicSettings(
        videoExtensions: ".mp4,.mkv,.mov",
        playWeight: 2.0,
        shortFeedMaxDurationMinutes: 5,
        theme: "system",
        deeplApiKeyConfigured: false,
        aiTaggingApiKeyConfigured: false,
        aiTaggingFrameCount: 5,
        aiTaggingSubtitleCharLimit: 4000,
        aiTaggingStartupBatchSize: 10
    )

    var body: some View {
        NavigationSplitView {
            List(selection: $selection) {
                Section("Library") {
                    Label("Videos", systemImage: "film.stack")
                    Label("Tags", systemImage: "tag")
                    Label("Directories", systemImage: "folder")
                }
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 220)
        } content: {
            VStack(spacing: 0) {
                toolbar
                Table(filteredVideos, selection: $selection) {
                    TableColumn("Name") { video in
                        VStack(alignment: .leading, spacing: 4) {
                            Text(video.name)
                                .font(.body)
                            Text(video.directory)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                    TableColumn("Tags") { video in
                        FlowTags(tags: tagsFor(video))
                    }
                    TableColumn("Resolution") { video in
                        Text(video.resolution.isEmpty ? "-" : video.resolution)
                    }
                    TableColumn("Score") { video in
                        Text(video.score, format: .number.precision(.fractionLength(1)))
                            .monospacedDigit()
                    }
                }
            }
        } detail: {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    previewPane
                    Divider()
                    operationsPane
                    Divider()
                    managementPane
                }
                .padding(18)
            }
        }
        .frame(minWidth: 1040, minHeight: 680)
        .onChange(of: selection) {
            renameText = selectedVideo?.nameWithoutExtension ?? ""
        }
    }

    private var filteredVideos: [NativeVideo] {
        let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return videos }
        return videos.filter { video in
            video.name.localizedCaseInsensitiveContains(trimmed)
                || video.path.localizedCaseInsensitiveContains(trimmed)
        }
    }

    private var selectedVideo: NativeVideo? {
        videos.first { $0.id == selection }
    }

    private var selectedVideoIndex: Int? {
        videos.firstIndex { $0.id == selection }
    }

    private var toolbar: some View {
        HStack(spacing: 10) {
            TextField("Search videos", text: $query)
                .textFieldStyle(.roundedBorder)
                .frame(minWidth: 260)
            Spacer()
            Button {
                statusMessage = "Preview route is available in Rust; live daemon client is not wired in this dev shell."
            } label: {
                Label("Preview", systemImage: "play.rectangle")
            }
            Button {
                statusMessage = "Playback route is available in Rust; live daemon client is not wired in this dev shell."
            } label: {
                Label("Play", systemImage: "play.fill")
            }
            Button {
                statusMessage = "Random play route is available in Rust; live daemon client is not wired in this dev shell."
            } label: {
                Label("Random", systemImage: "shuffle")
            }
        }
        .padding(12)
    }

    private var previewPane: some View {
        VStack(alignment: .leading, spacing: 14) {
            if let video = selectedVideo {
                HStack(alignment: .top) {
                    VStack(alignment: .leading, spacing: 4) {
                        Text(video.name)
                            .font(.title3)
                        Text(video.path)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(2)
                    }
                    Spacer()
                    if video.isStale {
                        Label("Stale", systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.orange)
                    }
                }

                ZStack {
                    Rectangle()
                        .fill(.quaternary)
                    Image(systemName: "play.rectangle.fill")
                        .font(.system(size: 48))
                        .foregroundStyle(.secondary)
                }
                .aspectRatio(16 / 9, contentMode: .fit)
                .clipShape(RoundedRectangle(cornerRadius: 8))

                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                    GridRow {
                        Text("Duration").foregroundStyle(.secondary)
                        Text(video.duration, format: .number.precision(.fractionLength(1)))
                    }
                    GridRow {
                        Text("Size").foregroundStyle(.secondary)
                        Text("\(video.size) bytes")
                    }
                    GridRow {
                        Text("Plays").foregroundStyle(.secondary)
                        Text("\(video.playCount) formal / \(video.randomPlayCount) random")
                    }
                }
                .font(.callout)

                FlowTags(tags: tagsFor(video))
            } else {
                ContentUnavailableView("No Selection", systemImage: "film")
            }
        }
    }

    private var operationsPane: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Video Operations")
                .font(.headline)

            HStack(spacing: 8) {
                TextField("New name", text: $renameText)
                    .textFieldStyle(.roundedBorder)
                Button {
                    renameSelectedVideo()
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                Button(role: .destructive) {
                    deleteSelectedVideo()
                } label: {
                    Label("Delete", systemImage: "trash")
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                Text("Tags")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                LazyVGrid(columns: [GridItem(.adaptive(minimum: 132), spacing: 8)], alignment: .leading, spacing: 8) {
                    ForEach(tags) { tag in
                        Toggle(isOn: bindingForTag(tag.id)) {
                            Label(tag.name, systemImage: "tag")
                        }
                        .toggleStyle(.button)
                    }
                }
                HStack(spacing: 8) {
                    TextField("Create tag", text: $newTagName)
                        .textFieldStyle(.roundedBorder)
                    Button {
                        createAndAssignTag()
                    } label: {
                        Label("Add Tag", systemImage: "plus")
                    }
                }
            }

            HStack(spacing: 8) {
                Button {
                    statusMessage = "Native subtitle indexing/search is implemented. WhisperX/Qwen extraction is still not wired into this SwiftUI shell."
                } label: {
                    Label("Subtitle", systemImage: "captions.bubble")
                }
                Button {
                    statusMessage = "AI tag review APIs are implemented. Model execution is not wired into this SwiftUI shell."
                } label: {
                    Label("AI Tags", systemImage: "sparkles")
                }
            }

            Text(statusMessage)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(3)
        }
    }

    private var managementPane: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Library Management")
                .font(.headline)

            HStack(alignment: .top, spacing: 24) {
                VStack(alignment: .leading, spacing: 8) {
                    Label("Tags", systemImage: "tag")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                    ForEach(tags) { tag in
                        HStack {
                            Circle()
                                .fill(Color(nsColor: .controlAccentColor))
                                .frame(width: 8, height: 8)
                            Text(tag.name)
                            Spacer()
                            Text(tag.color)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }
                }

                VStack(alignment: .leading, spacing: 8) {
                    Label("Directories", systemImage: "folder")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                    ForEach(directories) { directory in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(directory.alias.isEmpty ? directory.path : directory.alias)
                            Text(directory.path)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                }
            }

            Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                GridRow {
                    Text("Extensions").foregroundStyle(.secondary)
                    Text(settings.videoExtensions)
                }
                GridRow {
                    Text("Play Weight").foregroundStyle(.secondary)
                    Text(settings.playWeight, format: .number.precision(.fractionLength(1)))
                }
                GridRow {
                    Text("Secrets").foregroundStyle(.secondary)
                    Text(settings.deeplApiKeyConfigured || settings.aiTaggingApiKeyConfigured ? "Configured" : "Not configured")
                }
            }
            .font(.callout)
        }
    }

    private func tagsFor(_ video: NativeVideo) -> [VideoTagSummary] {
        tags
            .filter { video.tagIds.contains($0.id) }
            .map { VideoTagSummary(id: $0.id, name: $0.name, color: $0.color) }
    }

    private func bindingForTag(_ tagId: Int64) -> Binding<Bool> {
        Binding(
            get: {
                selectedVideo?.tagIds.contains(tagId) ?? false
            },
            set: { enabled in
                guard let index = selectedVideoIndex else { return }
                if enabled {
                    videos[index].tagIds.insert(tagId)
                    statusMessage = "Tag assigned locally. Rust API supports persistent assignment; live client wiring is next."
                } else {
                    videos[index].tagIds.remove(tagId)
                    statusMessage = "Tag removed locally. Rust API supports persistent removal; live client wiring is next."
                }
            }
        )
    }

    private func renameSelectedVideo() {
        guard let index = selectedVideoIndex else { return }
        let trimmed = renameText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else {
            statusMessage = "Name cannot be empty."
            return
        }
        videos[index].rename(stem: trimmed)
        statusMessage = "Renamed locally. Rust rename API is implemented; live client wiring is next."
    }

    private func deleteSelectedVideo() {
        guard let index = selectedVideoIndex else { return }
        let removed = videos.remove(at: index)
        selection = videos.first?.id
        statusMessage = "Deleted \(removed.name) from this local view. Rust delete API is implemented; live client wiring is next."
    }

    private func createAndAssignTag() {
        let trimmed = newTagName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, let index = selectedVideoIndex else { return }
        if let existing = tags.first(where: { $0.name.caseInsensitiveCompare(trimmed) == .orderedSame }) {
            videos[index].tagIds.insert(existing.id)
        } else {
            let nextId = (tags.map(\.id).max() ?? 0) + 1
            let tag = TagRecord(id: nextId, name: trimmed, color: "#3b82f6")
            tags.append(tag)
            videos[index].tagIds.insert(nextId)
        }
        newTagName = ""
        statusMessage = "Tag created and assigned locally. Persistent create/assign APIs are implemented in Rust."
    }
}

private struct NativeVideo: Identifiable, Hashable {
    let id: Int64
    var name: String
    var path: String
    var directory: String
    var size: Int64
    var duration: Double
    var resolution: String
    var width: Int
    var height: Int
    var isStale: Bool
    var playCount: Int
    var randomPlayCount: Int
    var score: Double
    var tagIds: Set<Int64>

    var nameWithoutExtension: String {
        NSString(string: name).deletingPathExtension
    }

    mutating func rename(stem: String) {
        let ext = NSString(string: name).pathExtension
        let newName = ext.isEmpty ? stem : "\(stem).\(ext)"
        name = newName
        path = (directory as NSString).appendingPathComponent(newName)
    }
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
                    .background(Color(nsColor: .controlBackgroundColor))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
        }
    }
}
