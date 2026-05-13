import AppKit
import AVKit
import CineInsightNativeCore
import SwiftUI

struct ContentView: View {
    @StateObject private var daemon: DaemonLifecycleManager
    @StateObject private var library: LibraryViewModel
    @AppStorage("CineInsightNative.appLanguage") private var appLanguageRaw = AppLanguage.zh.rawValue
    @State private var selection: SidebarSection = .library
    @State private var searchMode: SearchMode = .files
    @State private var activeLibraryTool: LibraryTool?
    @State private var scanDialogOpen = false
    @State private var scanDialogPath = ""
    @State private var scanDialogSummary: ScanImportSummary?
    @State private var previewDrawerOpen = false
    @State private var activeRowVideoID: Int64?
    @State private var renameText = ""
    @State private var newTagName = ""
    @State private var tagName = ""
    @State private var tagColor = ""
    @State private var editingTag: TagRecord?
    @State private var directoryPath = ""
    @State private var directoryAlias = ""
    @State private var editingDirectory: ScanDirectoryRecord?
    @State private var deleteFile = false
    @State private var settingsConfirmBeforeDelete = false
    @State private var settingsDeleteOriginalFile = false
    @State private var settingsVideoExtensions = ""
    @State private var settingsPlayWeight = 2.0
    @State private var settingsShortFeedMinutes = 5
    @State private var settingsTheme = "system"
    @State private var settingsDeeplApiKey = ""
    @State private var settingsAIBaseURL = ""
    @State private var settingsAIAPIKey = ""
    @State private var settingsAIModel = ""
    @State private var settingsAIFrameCount = 5
    @State private var settingsAISubtitleLimit = 4000
    @State private var settingsAIStartupBatch = 10
    @State private var settingsAutoScan = true
    @State private var settingsLogEnabled = false
    @State private var settingsBilingualEnabled = false
    @State private var settingsBilingualLang = "zh"
    @State private var subtitleEngine: SubtitleEngine = .whisperx
    @State private var subtitleSourceLang = "auto"

    private let client: NativeAPIClient

    private var appLanguage: AppLanguage {
        AppLanguage(rawValue: appLanguageRaw) ?? .zh
    }

    private var isChinese: Bool {
        appLanguage == .zh
    }

    private func t(_ zh: String, _ en: String) -> String {
        isChinese ? zh : en
    }

    init() {
        let configuration = DaemonLaunchConfiguration.defaultConfiguration()
        let client = NativeAPIClient(configuration: configuration)
        self.client = client
        _daemon = StateObject(wrappedValue: DaemonLifecycleManager())
        _library = StateObject(wrappedValue: LibraryViewModel(client: client))
    }

    var body: some View {
        VStack(spacing: 0) {
            appHeader
            Divider()
            HSplitView {
                contentColumn
                    .frame(minWidth: 720)

                if previewDrawerOpen && selection == .library {
                    Divider()
                    previewDrawer
                }
            }
        }
        .frame(minWidth: 1120, minHeight: 720)
        .task {
            NSApplication.shared.windows.first?.title = "析微影策"
            daemon.launch(client.configuration)
            await daemon.refreshHealth(using: client)
            await library.loadAll()
        }
        .onChange(of: library.selectedVideoID) {
            renameText = library.selectedVideo?.nameWithoutExtension ?? ""
        }
        .onChange(of: library.query) {
            if searchMode == .files {
                Task { await library.search() }
            }
        }
        .onChange(of: library.subtitleQuery) {
            if searchMode == .subtitles {
                Task { await library.searchSubtitles() }
            }
        }
        .onChange(of: library.sizeFilter) {
            Task { await library.search() }
        }
        .onChange(of: library.resolutionFilter) {
            Task { await library.search() }
        }
        .onChange(of: library.settings) {
            syncSettingsForm()
        }
        .sheet(item: $activeLibraryTool) { tool in
            libraryToolSheet(tool)
        }
        .sheet(isPresented: $scanDialogOpen) {
            scanDialog
        }
    }

    private var appHeader: some View {
        HStack(spacing: 16) {
            Spacer(minLength: 68)
            Spacer()
            Picker("", selection: $selection) {
                Label(t("视频列表", "Library"), systemImage: "film.stack").tag(SidebarSection.library)
                Label(t("设置", "Settings"), systemImage: "gearshape").tag(SidebarSection.settings)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(width: 220)
        }
        .padding(.horizontal, 16)
        .frame(height: 54)
    }

    private var contentColumn: some View {
        VStack(spacing: 0) {
            switch selection {
            case .library:
                toolbar
                Divider()
                videoTable
            case .settings:
                settingsContent
            }
        }
        .frame(minWidth: 640)
    }

    private var previewDrawer: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    Text("Preview")
                        .font(.headline)
                    Spacer()
                    Button {
                        previewDrawerOpen = false
                    } label: {
                        Image(systemName: "xmark")
                    }
                    .help("Close preview")
                }
                if let video = library.selectedVideo {
                    Text(video.name)
                        .font(.subheadline.weight(.semibold))
                        .lineLimit(2)
                    Text(video.path)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
                previewPanel
            }
            .padding(18)
        }
        .frame(minWidth: 340, idealWidth: 420, maxWidth: 520)
    }

    private var toolbar: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 12) {
                Picker("Search Mode", selection: $searchMode) {
                    ForEach(SearchMode.allCases) { mode in
                        Text(mode.label(isChinese: isChinese)).tag(mode)
                    }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .frame(width: 210)

                searchField
                    .frame(maxWidth: 520)

                if searchMode == .files {
                    Picker("Size", selection: $library.sizeFilter) {
                        ForEach(VideoSizeFilter.allCases) { filter in
                            Text(sizeFilterLabel(filter)).tag(filter)
                        }
                    }
                    .pickerStyle(.menu)
                    .frame(width: 150)

                    Picker("Resolution", selection: $library.resolutionFilter) {
                        ForEach(VideoResolutionFilter.allCases) { filter in
                            Text(resolutionFilterLabel(filter)).tag(filter)
                        }
                    }
                    .pickerStyle(.menu)
                    .frame(width: 150)
                }

                Spacer(minLength: 0)
            }

            HStack(spacing: 10) {
                Button {
                    if searchMode == .files {
                        Task { await library.loadAll() }
                    } else {
                        Task { await library.searchSubtitles() }
                    }
                } label: {
                    Label(t("刷新", "Refresh"), systemImage: "arrow.clockwise")
                }

                Button {
                    Task { await library.playRandom() }
                } label: {
                    Label(t("随机播放", "Random"), systemImage: "shuffle")
                }

                Button {
                    library.toggleSelectAllVisible()
                } label: {
                    Label(library.allVisibleSelected ? t("取消全选", "Clear Page") : t("选择本页", "Select Page"), systemImage: "checklist")
                }
                .disabled(library.filteredVideos.isEmpty)

                Button(role: .destructive) {
                    Task { await library.deleteSelectedVideos(deleteFile: deleteFile) }
                } label: {
                    Label(t("批量删除", "Delete Selected"), systemImage: "trash")
                }
                .disabled(library.selectedVideoIDs.isEmpty)

                Button {
                    scanDialogPath = ""
                    scanDialogSummary = nil
                    scanDialogOpen = true
                } label: {
                    Label(t("扫描目录", "Scan Directories"), systemImage: "folder.badge.gearshape")
                }

                Button {
                    activeLibraryTool = .aiTags
                    Task { await library.refreshAITaggingStatus() }
                } label: {
                    Label(t("AI 标签审阅", "AI Tag Review"), systemImage: "sparkles")
                }

                Button {
                    activeLibraryTool = .cleanup
                    Task { await library.refreshCleanupStatus() }
                } label: {
                    Label(t("清理候选", "Cleanup"), systemImage: "trash")
                }

                Button {
                    activeLibraryTool = .tags
                } label: {
                    Label(t("标签管理", "Tag Manager"), systemImage: "tag")
                }
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
    }

    private var searchField: some View {
        HStack(spacing: 7) {
            Image(systemName: "magnifyingglass")
                .font(.system(size: 13, weight: .medium))
                .foregroundStyle(.secondary)
            TextField(searchMode.placeholder(isChinese: isChinese), text: searchTextBinding)
            .textFieldStyle(.plain)
            .font(.callout)
            .submitLabel(.search)
            if !searchTextBinding.wrappedValue.isEmpty {
                Button {
                    searchTextBinding.wrappedValue = ""
                    if searchMode == .files {
                        Task { await library.search() }
                    } else {
                        library.clearSubtitleSearch()
                    }
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 10)
        .frame(height: 32)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 7))
        .overlay {
            RoundedRectangle(cornerRadius: 7)
                .stroke(.separator, lineWidth: 0.5)
        }
    }

    private func sizeFilterLabel(_ filter: VideoSizeFilter) -> String {
        switch filter {
        case .all: return t("体积：全部", "Size: All")
        case .small: return t("小于 300 MB", "Under 300 MB")
        case .medium: return "300 MB - 1 GB"
        case .large: return t("大于 1 GB", "Over 1 GB")
        }
    }

    private func resolutionFilterLabel(_ filter: VideoResolutionFilter) -> String {
        switch filter {
        case .all: return t("分辨率：全部", "Resolution: All")
        case .sd: return t("低于 720p", "Below 720p")
        case .hd: return "720p"
        case .fullHD: return "1080p"
        case .ultraHD: return "4K+"
        }
    }

    private var searchTextBinding: Binding<String> {
        Binding {
            searchMode == .files ? library.query : library.subtitleQuery
        } set: { value in
            if searchMode == .files {
                library.query = value
            } else {
                library.subtitleQuery = value
            }
        }
    }

    private var videoTable: some View {
        VStack(spacing: 0) {
            if searchMode == .files {
                tagFilterBar
                if !library.selectedVideoIDs.isEmpty {
                    HStack(spacing: 10) {
                        Text("Selected \(library.selectedVideoIDs.count)")
                            .font(.callout)
                        Spacer()
                        Button {
                            activeLibraryTool = .tags
                        } label: {
                            Label("Batch Tags", systemImage: "tag")
                        }
                        Toggle("Delete original files", isOn: $deleteFile)
                        Button(role: .destructive) {
                            Task { await library.deleteSelectedVideos(deleteFile: deleteFile) }
                        } label: {
                            Label("Delete Selected", systemImage: "trash")
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
                    Divider()
                }
                videoTableBody
            } else {
                subtitleSearchResults
            }
        }
    }

    private var tagFilterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                Button {
                    Task { await library.clearTagFilter() }
                } label: {
                    Label("All", systemImage: library.selectedTagIDs.isEmpty ? "checkmark.circle.fill" : "circle")
                }
                .buttonStyle(.bordered)
                ForEach(library.tags) { tag in
                    Button {
                        Task { await library.toggleTagFilter(tag) }
                    } label: {
                        HStack(spacing: 6) {
                            Circle()
                                .fill(Color(hex: tag.color) ?? .accentColor)
                                .frame(width: 8, height: 8)
                            Text(tag.name)
                            if library.selectedTagIDs.contains(tag.id) {
                                Image(systemName: "checkmark")
                            }
                        }
                    }
                    .buttonStyle(.bordered)
                }
            }
            .padding(12)
        }
    }

    private var videoTableBody: some View {
        Table(library.filteredVideos, selection: $library.selectedVideoID) {
            TableColumn("") { video in
                Button {
                    library.toggleSelection(video)
                } label: {
                    Image(systemName: library.selectedVideoIDs.contains(video.id) ? "checkmark.circle.fill" : "circle")
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Select \(video.name)")
            }
            .width(36)
            TableColumn(t("名称", "Name")) { video in
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
            TableColumn(t("标签", "Tags")) { video in
                FlowTags(tags: video.tags)
            }
            TableColumn(t("分辨率", "Resolution")) { video in
                Text(video.resolution.isEmpty ? "-" : video.resolution)
            }
            TableColumn(t("时长", "Duration")) { video in
                Text(formatDuration(video.duration))
                    .monospacedDigit()
            }
            TableColumn(t("分数", "Score")) { video in
                Text(video.score, format: .number.precision(.fractionLength(1)))
                    .monospacedDigit()
            }
            TableColumn("") { video in
                rowActions(video)
            }
            .width(180)
        }
        .overlay {
            if library.isLoading && library.videos.isEmpty {
                ProgressView("Loading library")
            } else if library.filteredVideos.isEmpty {
                ContentUnavailableView("No Videos", systemImage: "film")
            }
        }
    }

    private func rowActions(_ video: VideoSummary) -> some View {
        HStack(spacing: 6) {
            Button {
                Task { await previewVideo(video) }
            } label: {
                Image(systemName: "play.rectangle")
            }
            .help("Preview")
            Button {
                Task { await library.play(video) }
            } label: {
                Image(systemName: "play.fill")
            }
            .help("Play")
            Menu {
                Button {
                    Task { await library.openDirectory(video) }
                } label: {
                    Label("Open Directory", systemImage: "folder")
                }
                Button {
                    beginRename(video)
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                Button {
                    activeRowVideoID = video.id
                    activeLibraryTool = .rowTags
                } label: {
                    Label("Edit Tags", systemImage: "tag")
                }
                Button {
                    library.selectedVideoID = video.id
                    activeLibraryTool = .rowSubtitles
                } label: {
                    Label("Generate Subtitles", systemImage: "captions.bubble")
                }
                Divider()
                Button(role: .destructive) {
                    Task { await library.delete(video, deleteFile: deleteFile) }
                } label: {
                    Label("Delete", systemImage: "trash")
                }
            } label: {
                Image(systemName: "ellipsis.circle")
            }
            .help("More actions")
        }
        .buttonStyle(.borderless)
    }

    private var subtitleSearchResults: some View {
        List(library.subtitleMatches, id: \.segment.index) { match in
            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text(match.video.name)
                        .font(.headline)
                    Spacer()
                    Text("\(match.segment.startTimeMs)ms - \(match.segment.endTimeMs)ms")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                Text(match.segment.text)
                    .font(.callout)
                Text(match.video.path)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            .padding(.vertical, 3)
        }
        .overlay {
            if library.subtitleMatches.isEmpty {
                ContentUnavailableView("No Subtitle Matches", systemImage: "captions.bubble")
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

    private func rowTagEditor(video: VideoSummary) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(video.name)
                .font(.headline)
                .lineLimit(2)
            LazyVGrid(columns: [GridItem(.adaptive(minimum: 132), spacing: 8)], alignment: .leading, spacing: 8) {
                ForEach(library.tags) { tag in
                    Toggle(isOn: tagBinding(tag, video: video)) {
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
                    Task { await library.createAndAssignTag(name: name, video: video) }
                } label: {
                    Label("Add Tag", systemImage: "plus")
                }
            }
        }
        .padding(18)
        .frame(minWidth: 520, minHeight: 320)
    }

    private var scanDialog: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(t("扫描视频目录", "Scan Video Directory"))
                .font(.headline)
            HStack(spacing: 10) {
                Button {
                    chooseScanDialogDirectory()
                } label: {
                    Label(t("选择目录", "Choose Directory"), systemImage: "folder")
                }
                Text(scanDialogPath.isEmpty ? t("尚未选择目录", "No directory selected") : scanDialogPath)
                    .font(.callout)
                    .foregroundStyle(scanDialogPath.isEmpty ? .secondary : .primary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            if library.isLoading {
                ProgressView(t("正在扫描...", "Scanning..."))
            }
            if let summary = scanDialogSummary {
                Text(t("扫描完成：发现 \(summary.found) 个视频，新增 \(summary.imported) 个，删除 \(summary.deleted) 个，跳过 \(summary.skipped) 个。", "Scan complete: found \(summary.found), added \(summary.imported), deleted \(summary.deleted), skipped \(summary.skipped)."))
                    .font(.callout.weight(.medium))
                    .foregroundStyle(.green)
            }
            Text(t("扫描完成后会自动把该目录加入扫描目录配置。", "After scanning, the directory is saved to scan directory settings."))
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack {
                Spacer()
                Button(scanDialogSummary == nil ? t("取消", "Cancel") : t("关闭", "Close")) {
                    scanDialogOpen = false
                }
                Button {
                    Task {
                        scanDialogSummary = await library.scanAndImportDirectory(path: scanDialogPath)
                    }
                } label: {
                    Label(scanDialogSummary == nil ? t("开始扫描", "Start Scan") : t("重新扫描", "Scan Again"), systemImage: "magnifyingglass")
                }
                .disabled(scanDialogPath.isEmpty || library.isLoading)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(22)
        .frame(width: 520)
    }

    private var directoriesPanel: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(t("扫描目录管理", "Scan Directories"))
                .font(.headline)
            VStack(spacing: 0) {
                if library.directories.isEmpty {
                    Text(t("暂无扫描目录配置", "No scan directories configured."))
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.vertical, 12)
                } else {
                    ForEach(library.directories) { directory in
                        HStack(spacing: 12) {
                            VStack(alignment: .leading, spacing: 3) {
                                Text(directory.alias.isEmpty ? directory.path : directory.alias)
                                    .font(.callout.weight(.medium))
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
                        .padding(.vertical, 8)
                        if directory.id != library.directories.last?.id {
                            Divider()
                        }
                    }
                }
            }
            Divider()
            HStack(spacing: 8) {
                TextField(t("目录路径", "Path"), text: $directoryPath)
                    .textFieldStyle(.roundedBorder)
                Button {
                    chooseDirectory()
                } label: {
                    Image(systemName: "folder")
                }
                TextField(t("目录别名", "Alias"), text: $directoryAlias)
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
                    Label(editingDirectory == nil ? t("添加", "Add") : t("更新", "Update"), systemImage: editingDirectory == nil ? "plus" : "checkmark")
                }
                Button {
                    editingDirectory = nil
                    directoryPath = ""
                    directoryAlias = ""
                } label: {
                    Label(t("取消", "Cancel"), systemImage: "xmark")
                }
                .disabled(editingDirectory == nil && directoryPath.isEmpty && directoryAlias.isEmpty)
            }
        }
        .settingsSectionStyle()
    }

    private var subtitlesPanel: some View {
        VStack(spacing: 0) {
            subtitleGenerationPanel
                .padding(12)
            Divider()
            subtitleSearchResults
        }
    }

    private var subtitleGenerationPanel: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Subtitles")
                .font(.headline)
            HStack(spacing: 10) {
                Picker("Engine", selection: $subtitleEngine) {
                    Text("WhisperX").tag(SubtitleEngine.whisperx)
                    Text("Qwen").tag(SubtitleEngine.qwen)
                }
                .pickerStyle(.segmented)
                TextField("Source language", text: $subtitleSourceLang)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 118)
            }
            HStack(spacing: 8) {
                Button {
                    Task { await library.prepareSubtitleEngine(subtitleEngine) }
                } label: {
                    Label("Prepare", systemImage: "arrow.down.circle")
                }
                Button {
                    Task { await library.generateSubtitle(engine: subtitleEngine, sourceLang: subtitleSourceLang) }
                } label: {
                    Label("Generate", systemImage: "captions.bubble")
                }
                .disabled(library.selectedVideo == nil)
                Button {
                    Task { await library.forceGenerateSubtitle(engine: subtitleEngine, sourceLang: subtitleSourceLang) }
                } label: {
                    Label("Force", systemImage: "exclamationmark.triangle")
                }
                .disabled(library.selectedVideo == nil || library.lastSubtitleResult?.forceEligible != true)
                Button {
                    Task { await library.cancelSubtitle() }
                } label: {
                    Image(systemName: "xmark.circle")
                }
                .help("Cancel subtitle job")
                .disabled(library.subtitleJobStatus?.progress.cancellable != true)
            }
            if let status = library.subtitleJobStatus {
                HStack(spacing: 10) {
                    ProgressView(value: Double(status.progress.percent), total: 100)
                    Text(status.progress.phase)
                        .font(.caption)
                        .frame(width: 92, alignment: .leading)
                    Text(status.progress.message)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            if !library.subtitleEngines.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(library.subtitleEngines, id: \.engine) { engine in
                        Label(engine.available ? "\(engine.displayName) ready" : "\(engine.displayName): \(engine.reasonCode)", systemImage: engine.available ? "checkmark.circle" : "exclamationmark.circle")
                            .font(.caption)
                            .foregroundStyle(engine.available ? .green : .secondary)
                    }
                }
            }
        }
    }

    private var aiTagsPanel: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Pending review \(library.aiCandidates.filter { $0.status == .pending }.count)")
                    .font(.headline)
                if let status = library.aiTaggingStatus {
                    Text("Pending \(status.pending) · Done \(status.completed) · Failed \(status.failed)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Button {
                    Task { await library.refreshAITaggingStatus() }
                } label: {
                    Label("Refresh", systemImage: "arrow.clockwise")
                }
            }
            .padding(12)
            Divider()
            List(library.aiCandidateGroups) { group in
                Section {
                    ForEach(group.candidates) { candidate in
                        aiCandidateRow(candidate)
                    }
                } header: {
                    VStack(alignment: .leading, spacing: 3) {
                        HStack {
                            Text(group.videoName)
                                .font(.headline)
                            Spacer()
                            Text("\(group.pendingCount) pending")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Button {
                                Task { await library.retryAITagging(videoId: group.videoId) }
                            } label: {
                                Label("Retry", systemImage: "arrow.clockwise")
                            }
                            Button(role: .destructive) {
                                Task { await library.rejectPendingCandidates(videoId: group.videoId) }
                            } label: {
                                Label("Reject Pending", systemImage: "xmark.circle")
                            }
                            .disabled(group.pendingCount == 0)
                        }
                        if !group.videoPath.isEmpty {
                            Text(group.videoPath)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                }
            }
            .overlay {
                if library.aiCandidateGroups.isEmpty {
                    ContentUnavailableView("No AI Tag Candidates", systemImage: "sparkles")
                }
            }
        }
    }

    private func aiCandidateRow(_ candidate: AITagCandidateRecord) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text(candidate.confidence.uppercased())
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(confidenceColor(candidate.confidence).opacity(0.18))
                    .foregroundStyle(confidenceColor(candidate.confidence))
                    .clipShape(RoundedRectangle(cornerRadius: 6))
                Text(candidate.suggestedName)
                    .font(.headline)
                Spacer()
                Text(candidate.status.rawValue)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            if !candidate.reasoning.isEmpty {
                Text(candidate.reasoning)
                    .font(.callout)
            }
            if !candidate.sourceSummary.isEmpty {
                Text(candidate.sourceSummary)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
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

    private var cleanupPanel: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .top, spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(t("清理候选审阅", "Cleanup Review"))
                        .font(.headline)
                    Text(t("基于重复、过短和低清规则生成候选。先分析，再预览候选，最后再决定是否删除。", "Analyze duplicate, short, and low-resolution candidates before deleting anything."))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                }
                Spacer()
                Button {
                    activeLibraryTool = nil
                } label: {
                    Image(systemName: "xmark")
                }
                .help(t("关闭", "Close"))
            }
            HStack(spacing: 10) {
                Button {
                    Task { await library.startCleanup() }
                } label: {
                    Label(t("开始分析", "Analyze"), systemImage: "wand.and.stars")
                }
                Button {
                    Task { await library.refreshCleanupStatus() }
                } label: {
                    Label(t("刷新状态", "Refresh"), systemImage: "arrow.clockwise")
                }
                Spacer()
                cleanupStatusSummary
            }
            if let cleanup = library.cleanup {
                HStack(spacing: 8) {
                    cleanupMetric(t("重复组", "Duplicate Groups"), cleanup.duplicateGroups.count)
                    cleanupMetric(t("短视频", "Short Videos"), cleanup.lowDurationIds.count)
                    cleanupMetric(t("低清视频", "Low Resolution"), cleanup.lowResolutionIds.count)
                }
                List {
                    if !cleanup.duplicateGroups.isEmpty {
                        Section(t("重复候选", "Duplicate Candidates")) {
                            ForEach(cleanup.duplicateGroups, id: \.originalId) { group in
                                VStack(alignment: .leading, spacing: 4) {
                                    Text(group.reason)
                                    Text(t("建议保留 #\(group.originalId)，审阅 \(group.candidateIds.map { "#\($0)" }.joined(separator: ", "))", "Keep #\(group.originalId), review \(group.candidateIds.map { "#\($0)" }.joined(separator: ", "))"))
                                        .font(.caption)
                                        .foregroundStyle(.secondary)
                                }
                            }
                        }
                    }
                    if !library.cleanupCandidateVideos.isEmpty {
                        Section(t("当前页匹配视频", "Matched Videos In Current Page")) {
                            ForEach(library.cleanupCandidateVideos) { video in
                                HStack {
                                    VStack(alignment: .leading, spacing: 3) {
                                        Text(video.name)
                                        Text("\(formatDuration(video.duration)) · \(video.resolution.isEmpty ? "-" : video.resolution) · \(video.path)")
                                            .font(.caption)
                                            .foregroundStyle(.secondary)
                                            .lineLimit(1)
                                    }
                                    Spacer()
                                    Button {
                                        library.selectedVideoID = video.id
                                        Task { await library.previewExternally() }
                                    } label: {
                                        Label(t("预览", "Preview"), systemImage: "play.rectangle")
                                    }
                                }
                            }
                        }
                    }
                }
            } else {
                VStack(spacing: 12) {
                    Image(systemName: "trash")
                        .font(.system(size: 34, weight: .regular))
                        .foregroundStyle(.tertiary)
                    Text(t("还没有清理分析", "No Cleanup Analysis"))
                        .font(.title3.weight(.semibold))
                    Text(t("点击“开始分析”后，会列出重复、过短和低清候选。", "Click Analyze to list duplicate, short, and low-resolution candidates."))
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .padding(18)
    }

    @ViewBuilder
    private var cleanupStatusSummary: some View {
        if let cleanup = library.cleanup {
            Text(t("候选 \(cleanup.allCandidateIds.count)", "Candidates \(cleanup.allCandidateIds.count)"))
                .font(.callout)
                .foregroundStyle(.secondary)
        } else if let status = library.cleanupStatus, !status.progress.message.isEmpty {
            Text(status.progress.message)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    private func cleanupMetric(_ label: String, _ value: Int) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("\(value)")
                .font(.title3.monospacedDigit())
            Text(label)
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(.quaternary, in: RoundedRectangle(cornerRadius: 8))
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

    private var settingsContent: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 18) {
                Text(t("设置", "Settings"))
                    .font(.title2.weight(.semibold))
                settingsPanel
                directoriesPanel
                shortFeedSettingsBlock
                diagnosticsSummaryPanel
            }
            .padding(18)
            .frame(maxWidth: 980, alignment: .leading)
        }
    }

    private var diagnosticsSummaryPanel: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text(t("诊断", "Diagnostics"))
                    .font(.headline)
                Spacer()
                Button {
                    Task { await library.refreshDiagnostics() }
                } label: {
                    Label(t("刷新", "Refresh"), systemImage: "arrow.clockwise")
                }
            }
            if let diagnostics = library.diagnostics {
                Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 8) {
                    metricRow(t("视频", "Videos"), "\(diagnostics.videoCount)")
                    metricRow(t("标签", "Tags"), "\(diagnostics.tagCount)")
                    metricRow(t("字幕", "Subtitles"), "\(diagnostics.subtitleSegmentCount)")
                    metricRow(t("AI 候选", "AI Candidates"), "\(diagnostics.aiCandidateCount)")
                    metricRow(t("短视频", "Short Feed"), "\(diagnostics.shortFeedInteractionCount)")
                }
                .font(.callout)
            } else {
                Text(t("诊断不可用", "Diagnostics unavailable"))
                    .foregroundStyle(.secondary)
            }
        }
        .settingsSectionStyle()
    }

    private var settingsPanel: some View {
        VStack(alignment: .leading, spacing: 16) {
            if let settings = library.settings {
                settingsGroup(t("基本设置", "General")) {
                    Picker(t("界面语言", "Interface Language"), selection: $appLanguageRaw) {
                        Text("中文").tag(AppLanguage.zh.rawValue)
                        Text("English").tag(AppLanguage.en.rawValue)
                    }
                    .pickerStyle(.segmented)
                    Toggle(t("删除前确认", "Confirm before delete"), isOn: $settingsConfirmBeforeDelete)
                    Toggle(t("默认将原始文件移入回收站", "Move original file to Trash by default"), isOn: $settingsDeleteOriginalFile)
                    Toggle(t("启用日志记录", "Enable frontend logging"), isOn: $settingsLogEnabled)
                    Picker(t("主题模式", "Theme"), selection: $settingsTheme) {
                        Text(t("跟随系统", "System")).tag("system")
                        Text(t("浅色", "Light")).tag("light")
                        Text(t("深色", "Dark")).tag("dark")
                    }
                    .pickerStyle(.segmented)
                }

                settingsGroup(t("自动化与扫描", "Automation & Scan")) {
                    Toggle(t("启动时自动增量扫描", "Start incremental scan on launch"), isOn: $settingsAutoScan)
                }

                settingsGroup(t("手机短视频", "Mobile Short Feed")) {
                    Stepper(t("短视频时长上限：\(settingsShortFeedMinutes) 分钟", "Max duration: \(settingsShortFeedMinutes) min"), value: $settingsShortFeedMinutes, in: 1...180)
                }

                settingsGroup(t("AI 标签", "AI Tags")) {
                    TextField("https://api.openai.com/v1", text: $settingsAIBaseURL)
                        .textFieldStyle(.roundedBorder)
                    SecureField(settings.aiTaggingApiKeyConfigured ? t("已配置 API Key，留空则保留", "API key configured, leave blank to keep") : "API Key", text: $settingsAIAPIKey)
                        .textFieldStyle(.roundedBorder)
                    TextField(t("模型", "Model"), text: $settingsAIModel)
                        .textFieldStyle(.roundedBorder)
                    Stepper(t("抽帧数量：\(settingsAIFrameCount)", "Frames: \(settingsAIFrameCount)"), value: $settingsAIFrameCount, in: 1...8)
                    Stepper(t("字幕字符上限：\(settingsAISubtitleLimit)", "Subtitle limit: \(settingsAISubtitleLimit)"), value: $settingsAISubtitleLimit, in: 200...12_000, step: 100)
                    Stepper(t("后台批量数量：\(settingsAIStartupBatch)", "Startup batch: \(settingsAIStartupBatch)"), value: $settingsAIStartupBatch, in: 1...100)
                }

                settingsGroup(t("智能随机播放", "Smart Random Play")) {
                    HStack {
                        Text(t("播放权重", "Play Weight"))
                        Slider(value: $settingsPlayWeight, in: 0.1...10, step: 0.1)
                        Text(String(format: "%.1f", settingsPlayWeight))
                            .monospacedDigit()
                            .frame(width: 44, alignment: .trailing)
                    }
                }

                settingsGroup(t("支持的视频格式", "Video Extensions")) {
                    TextField(".mp4,.mkv,.mov", text: $settingsVideoExtensions)
                        .textFieldStyle(.roundedBorder)
                }

                settingsGroup(t("字幕翻译", "Subtitle Translation")) {
                    Toggle(t("启用双语字幕翻译", "Enable bilingual subtitle translation"), isOn: $settingsBilingualEnabled)
                    if settingsBilingualEnabled {
                        Picker(t("目标翻译语言", "Target language"), selection: $settingsBilingualLang) {
                            Text(t("中文", "Chinese")).tag("zh")
                            Text(t("英语", "English")).tag("en")
                            Text(t("日语", "Japanese")).tag("ja")
                            Text(t("韩语", "Korean")).tag("ko")
                            Text(t("法语", "French")).tag("fr")
                            Text(t("德语", "German")).tag("de")
                            Text(t("西班牙语", "Spanish")).tag("es")
                            Text(t("葡萄牙语", "Portuguese")).tag("pt")
                            Text(t("俄语", "Russian")).tag("ru")
                            Text(t("意大利语", "Italian")).tag("it")
                        }
                        .pickerStyle(.menu)
                        SecureField(settings.deeplApiKeyConfigured ? t("已配置 DeepL Key，留空则保留", "DeepL key configured, leave blank to keep") : "DeepL API Key", text: $settingsDeeplApiKey)
                            .textFieldStyle(.roundedBorder)
                    }
                }

                Button {
                    Task {
                        await library.saveSettings(
                            confirmBeforeDelete: settingsConfirmBeforeDelete,
                            deleteOriginalFile: settingsDeleteOriginalFile,
                            videoExtensions: settingsVideoExtensions,
                            playWeight: settingsPlayWeight,
                            shortFeedMaxDurationMinutes: settingsShortFeedMinutes,
                            theme: settingsTheme,
                            autoScanOnStartup: settingsAutoScan,
                            logEnabled: settingsLogEnabled,
                            bilingualEnabled: settingsBilingualEnabled,
                            bilingualLang: settingsBilingualLang,
                            deeplApiKey: settingsDeeplApiKey,
                            aiTaggingBaseUrl: settingsAIBaseURL,
                            aiTaggingApiKey: settingsAIAPIKey,
                            aiTaggingModel: settingsAIModel,
                            aiFrameCount: settingsAIFrameCount,
                            aiSubtitleCharLimit: settingsAISubtitleLimit,
                            aiStartupBatchSize: settingsAIStartupBatch
                        )
                        settingsDeeplApiKey = ""
                        settingsAIAPIKey = ""
                    }
                } label: {
                    Label(t("保存所有设置", "Save Settings"), systemImage: "checkmark")
                }
            } else {
                Text(t("无法连接服务器。", "Settings unavailable"))
                    .foregroundStyle(.secondary)
            }
            Text(library.statusMessage)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(3)
        }
        .settingsSectionStyle()
    }

    @ViewBuilder
    private func settingsGroup<Content: View>(_ title: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.headline)
            content()
        }
    }

    private var shortFeedSettingsBlock: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Label(t("手机短视频", "Mobile Short Feed"), systemImage: "iphone")
                    .font(.subheadline.weight(.semibold))
                Spacer()
                Button {
                    Task { await library.refreshShortFeedStatus() }
                } label: {
                    Label(t("刷新", "Refresh"), systemImage: "arrow.clockwise")
                }
            }

            if let status = library.shortFeedStatus {
                Label(status.running ? t("运行中", "Running") : t("未运行", "Unavailable"), systemImage: status.running ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                    .foregroundStyle(status.running ? .green : .orange)
                if !status.url.isEmpty {
                    shortFeedURLRow(label: status.fallbackUsed ? t("本机地址（备用端口）", "Local URL (fallback port)") : t("本机地址", "Local URL"), url: status.url)
                } else if !status.startupError.isEmpty {
                    Text(status.startupError)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                ForEach(status.lanUrls, id: \.self) { url in
                    shortFeedURLRow(label: t("局域网地址", "LAN URL"), url: url)
                }
                Text(status.allowedAccess)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            } else {
                Text(t("短视频服务状态尚未加载。", "Short Feed server status has not been loaded."))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .settingsSectionStyle()
    }

    private func shortFeedURLRow(label: String, url: String) -> some View {
        HStack(spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Text(url)
                    .font(.caption.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer()
            Button {
                openExternalURL(url)
            } label: {
                Image(systemName: "safari")
            }
            .help(t("在浏览器打开", "Open in browser"))
            Button {
                copyToClipboard(url)
            } label: {
                Image(systemName: "doc.on.doc")
            }
            .help(t("复制地址", "Copy URL"))
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
        guard let video = library.selectedVideo else {
            return .constant(false)
        }
        return tagBinding(tag, video: video)
    }

    private func tagBinding(_ tag: TagRecord, video: VideoSummary) -> Binding<Bool> {
        Binding {
            library.videos.first(where: { $0.id == video.id })?.tags.contains { $0.id == tag.id } ?? false
        } set: { enabled in
            Task { await library.setTag(tag, enabled: enabled, video: video) }
        }
    }

    private func previewVideo(_ video: VideoSummary) async {
        await library.loadPreview(for: video)
        previewDrawerOpen = true
    }

    private func beginRename(_ video: VideoSummary) {
        library.selectedVideoID = video.id
        renameText = video.nameWithoutExtension
        activeLibraryTool = .rename
    }

    @ViewBuilder
    private func libraryToolSheet(_ tool: LibraryTool) -> some View {
        switch tool {
        case .tags:
            tagsPanel
                .frame(minWidth: 620, minHeight: 460)
        case .aiTags:
            aiTagsPanel
                .frame(minWidth: 760, minHeight: 560)
        case .cleanup:
            cleanupPanel
                .frame(width: 760, height: 560)
        case .rowTags:
            if let video = activeRowVideo {
                rowTagEditor(video: video)
            } else {
                ContentUnavailableView("No Video", systemImage: "film")
                    .frame(minWidth: 420, minHeight: 240)
            }
        case .rowSubtitles:
            subtitleGenerationPanel
                .padding(18)
                .frame(minWidth: 520)
        case .rename:
            renameSheet
        }
    }

    private var activeRowVideo: VideoSummary? {
        guard let activeRowVideoID else { return library.selectedVideo }
        return library.videos.first { $0.id == activeRowVideoID }
    }

    private var renameSheet: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Rename Video")
                .font(.headline)
            Text(library.selectedVideo?.name ?? "")
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
            TextField("New filename", text: $renameText)
                .textFieldStyle(.roundedBorder)
            HStack {
                Spacer()
                Button("Cancel") {
                    activeLibraryTool = nil
                }
                Button("Rename") {
                    if let video = library.selectedVideo {
                        Task {
                            await library.rename(video, to: renameText)
                            activeLibraryTool = nil
                        }
                    }
                }
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(18)
        .frame(width: 420)
    }

    private func syncSettingsForm() {
        guard let settings = library.settings else { return }
        settingsVideoExtensions = settings.videoExtensions
        settingsPlayWeight = settings.playWeight
        settingsShortFeedMinutes = settings.shortFeedMaxDurationMinutes
        settingsTheme = settings.theme
        settingsConfirmBeforeDelete = settings.confirmBeforeDelete
        settingsDeleteOriginalFile = settings.deleteOriginalFile
        settingsAIFrameCount = settings.aiTaggingFrameCount
        settingsAISubtitleLimit = settings.aiTaggingSubtitleCharLimit
        settingsAIStartupBatch = settings.aiTaggingStartupBatchSize
        settingsAIBaseURL = settings.aiTaggingBaseUrl
        settingsAIModel = settings.aiTaggingModel
        settingsAutoScan = settings.autoScanOnStartup
        settingsLogEnabled = settings.logEnabled
        settingsBilingualEnabled = settings.bilingualEnabled
        settingsBilingualLang = settings.bilingualLang
    }

    private func confidenceColor(_ confidence: String) -> Color {
        switch confidence.lowercased() {
        case "high":
            return .green
        case "medium":
            return .orange
        case "low":
            return .red
        default:
            return .secondary
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

    private func chooseScanDialogDirectory() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        if panel.runModal() == .OK, let path = panel.url?.path {
            scanDialogPath = path
            scanDialogSummary = nil
        }
    }

    private func openExternalURL(_ value: String) {
        guard let url = URL(string: value) else { return }
        NSWorkspace.shared.open(url)
    }

    private func copyToClipboard(_ value: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(value, forType: .string)
    }

    private func formatDuration(_ seconds: Double) -> String {
        guard seconds.isFinite, seconds > 0 else { return "-" }
        let total = Int(seconds.rounded())
        return String(format: "%02d:%02d", total / 60, total % 60)
    }

}

private enum SidebarSection: Hashable {
    case library
    case settings
}

private enum AppLanguage: String {
    case zh
    case en
}

private enum SearchMode: String, CaseIterable, Identifiable {
    case files
    case subtitles

    var id: String { rawValue }

    func label(isChinese: Bool) -> String {
        switch self {
        case .files: return isChinese ? "文件搜索" : "Files"
        case .subtitles: return isChinese ? "字幕搜索" : "Subtitles"
        }
    }

    func placeholder(isChinese: Bool) -> String {
        switch self {
        case .files: return isChinese ? "搜索视频文件名、路径或标签" : "Search videos, paths, or tags"
        case .subtitles: return isChinese ? "搜索字幕内容" : "Search subtitle text"
        }
    }
}

private enum LibraryTool: String, Identifiable {
    case tags
    case aiTags
    case cleanup
    case rowTags
    case rowSubtitles
    case rename

    var id: String { rawValue }
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

private struct SettingsSectionStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .padding(12)
            .background(.background)
            .overlay {
                RoundedRectangle(cornerRadius: 8)
                    .stroke(.separator, lineWidth: 0.5)
            }
    }
}

private extension View {
    func settingsSectionStyle() -> some View {
        modifier(SettingsSectionStyle())
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
