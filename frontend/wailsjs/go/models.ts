export namespace models {

	export class MediaCollection {
	    id: number;
	    name: string;
	    description: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new MediaCollection(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class MediaStream {
	    id: number;
	    video_id: number;
	    stream_index: number;
	    stream_type: string;
	    codec_name: string;
	    codec_long_name: string;
	    profile: string;
	    bit_rate?: number;
	    language: string;
	    title: string;
	    is_default: boolean;
	    width?: number;
	    height?: number;
	    avg_frame_rate: string;
	    real_frame_rate: string;
	    pixel_format: string;
	    bits_per_raw_sample?: number;
	    color_range: string;
	    color_space: string;
	    color_transfer: string;
	    color_primaries: string;
	    is_hdr?: boolean;
	    is_attached_pic: boolean;
	    sample_rate?: number;
	    channels?: number;
	    channel_layout: string;
	    created_at: string;

	    static createFrom(source: any = {}) {
	        return new MediaStream(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.video_id = source["video_id"];
	        this.stream_index = source["stream_index"];
	        this.stream_type = source["stream_type"];
	        this.codec_name = source["codec_name"];
	        this.codec_long_name = source["codec_long_name"];
	        this.profile = source["profile"];
	        this.bit_rate = source["bit_rate"];
	        this.language = source["language"];
	        this.title = source["title"];
	        this.is_default = source["is_default"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.avg_frame_rate = source["avg_frame_rate"];
	        this.real_frame_rate = source["real_frame_rate"];
	        this.pixel_format = source["pixel_format"];
	        this.bits_per_raw_sample = source["bits_per_raw_sample"];
	        this.color_range = source["color_range"];
	        this.color_space = source["color_space"];
	        this.color_transfer = source["color_transfer"];
	        this.color_primaries = source["color_primaries"];
	        this.is_hdr = source["is_hdr"];
	        this.is_attached_pic = source["is_attached_pic"];
	        this.sample_rate = source["sample_rate"];
	        this.channels = source["channels"];
	        this.channel_layout = source["channel_layout"];
	        this.created_at = source["created_at"];
	    }
	}
	export class Person {
	    id: number;
	    display_name: string;
	    original_name: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new Person(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.display_name = source["display_name"];
	        this.original_name = source["original_name"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class SavedLibraryView {
	    id: number;
	    name: string;
	    search_mode: string;
	    keyword: string;
	    smart_view: string;
	    tag_ids_json: string;
	    min_size: number;
	    max_size: number;
	    min_height: number;
	    max_height: number;
	    min_rating?: number;
	    max_rating?: number;
	    sort_mode: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new SavedLibraryView(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.search_mode = source["search_mode"];
	        this.keyword = source["keyword"];
	        this.smart_view = source["smart_view"];
	        this.tag_ids_json = source["tag_ids_json"];
	        this.min_size = source["min_size"];
	        this.max_size = source["max_size"];
	        this.min_height = source["min_height"];
	        this.max_height = source["max_height"];
	        this.min_rating = source["min_rating"];
	        this.max_rating = source["max_rating"];
	        this.sort_mode = source["sort_mode"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class ScanDirectory {
	    id: number;
	    path: string;
	    alias: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new ScanDirectory(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.alias = source["alias"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class Settings {
	    id: number;
	    confirm_before_delete: boolean;
	    delete_original_file: boolean;
	    video_extensions: string;
	    play_weight: number;
	    auto_scan_on_startup: boolean;
	    library_watch_enabled: boolean;
	    short_feed_max_duration_minutes: number;
	    theme: string;
	    log_enabled: boolean;
	    bilingual_enabled: boolean;
	    bilingual_lang: string;
	    deepl_api_key: string;
	    subtitle_translation_provider: string;
	    subtitle_translation_base_url: string;
	    subtitle_translation_api_key: string;
	    subtitle_translation_model: string;
	    subtitle_whisperx_model: string;
	    subtitle_whisperx_batch_size: number;
	    ai_tagging_base_url: string;
	    ai_tagging_api_key: string;
	    ai_tagging_model: string;
	    ai_tagging_frame_count: number;
	    ai_tagging_images_per_request: number;
	    ai_tagging_subtitle_char_limit: number;
	    ai_tagging_startup_batch_size: number;
	    ai_tagging_max_extra_frames: number;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.confirm_before_delete = source["confirm_before_delete"];
	        this.delete_original_file = source["delete_original_file"];
	        this.video_extensions = source["video_extensions"];
	        this.play_weight = source["play_weight"];
	        this.auto_scan_on_startup = source["auto_scan_on_startup"];
	        this.library_watch_enabled = source["library_watch_enabled"];
	        this.short_feed_max_duration_minutes = source["short_feed_max_duration_minutes"];
	        this.theme = source["theme"];
	        this.log_enabled = source["log_enabled"];
	        this.bilingual_enabled = source["bilingual_enabled"];
	        this.bilingual_lang = source["bilingual_lang"];
	        this.deepl_api_key = source["deepl_api_key"];
	        this.subtitle_translation_provider = source["subtitle_translation_provider"];
	        this.subtitle_translation_base_url = source["subtitle_translation_base_url"];
	        this.subtitle_translation_api_key = source["subtitle_translation_api_key"];
	        this.subtitle_translation_model = source["subtitle_translation_model"];
	        this.subtitle_whisperx_model = source["subtitle_whisperx_model"];
	        this.subtitle_whisperx_batch_size = source["subtitle_whisperx_batch_size"];
	        this.ai_tagging_base_url = source["ai_tagging_base_url"];
	        this.ai_tagging_api_key = source["ai_tagging_api_key"];
	        this.ai_tagging_model = source["ai_tagging_model"];
	        this.ai_tagging_frame_count = source["ai_tagging_frame_count"];
	        this.ai_tagging_images_per_request = source["ai_tagging_images_per_request"];
	        this.ai_tagging_subtitle_char_limit = source["ai_tagging_subtitle_char_limit"];
	        this.ai_tagging_startup_batch_size = source["ai_tagging_startup_batch_size"];
	        this.ai_tagging_max_extra_frames = source["ai_tagging_max_extra_frames"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class Tag {
	    id: number;
	    name: string;
	    color: string;
	    namespace: string;
	    automatic_kind: string;
	    is_system: boolean;
	    is_active: boolean;
	    review_required: boolean;
	    sort_order: number;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.namespace = source["namespace"];
	        this.automatic_kind = source["automatic_kind"];
	        this.is_system = source["is_system"];
	        this.is_active = source["is_active"];
	        this.review_required = source["review_required"];
	        this.sort_order = source["sort_order"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class Video {
	    id: number;
	    name: string;
	    display_title: string;
	    original_title: string;
	    description: string;
	    personal_rating?: number;
	    path: string;
	    directory: string;
	    size: number;
	    duration: number;
	    resolution: string;
	    width: number;
	    height: number;
	    is_stale: boolean;
	    play_count: number;
	    random_play_count: number;
	    last_played_at?: string;
	    is_favorite: boolean;
	    is_watched: boolean;
	    watch_position_seconds: number;
	    watch_progress_updated_at?: string;
	    watched_at?: string;
	    tags: Tag[];
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new Video(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.display_title = source["display_title"];
	        this.original_title = source["original_title"];
	        this.description = source["description"];
	        this.personal_rating = source["personal_rating"];
	        this.path = source["path"];
	        this.directory = source["directory"];
	        this.size = source["size"];
	        this.duration = source["duration"];
	        this.resolution = source["resolution"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.is_stale = source["is_stale"];
	        this.play_count = source["play_count"];
	        this.random_play_count = source["random_play_count"];
	        this.last_played_at = source["last_played_at"];
	        this.is_favorite = source["is_favorite"];
	        this.is_watched = source["is_watched"];
	        this.watch_position_seconds = source["watch_position_seconds"];
	        this.watch_progress_updated_at = source["watch_progress_updated_at"];
	        this.watched_at = source["watched_at"];
	        this.tags = this.convertValues(source["tags"], Tag);
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VideoTechnicalMetadata {
	    video_id: number;
	    format_name: string;
	    format_long_name: string;
	    total_bit_rate?: number;
	    successful_source_size?: number;
	    successful_source_mod_time_ns?: number;
	    probed_at?: string;
	    last_attempt_source_size?: number;
	    last_attempt_source_mod_time_ns?: number;
	    last_attempt_at?: string;
	    last_error: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new VideoTechnicalMetadata(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.format_name = source["format_name"];
	        this.format_long_name = source["format_long_name"];
	        this.total_bit_rate = source["total_bit_rate"];
	        this.successful_source_size = source["successful_source_size"];
	        this.successful_source_mod_time_ns = source["successful_source_mod_time_ns"];
	        this.probed_at = source["probed_at"];
	        this.last_attempt_source_size = source["last_attempt_source_size"];
	        this.last_attempt_source_mod_time_ns = source["last_attempt_source_mod_time_ns"];
	        this.last_attempt_at = source["last_attempt_at"];
	        this.last_error = source["last_error"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class VideoTrashEntry {
	    id: number;
	    video_id: number;
	    video_name: string;
	    original_path: string;
	    trash_path: string;
	    file_moved: boolean;
	    file_size: number;
	    file_mod_time: number;
	    state: string;
	    last_error: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new VideoTrashEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.video_id = source["video_id"];
	        this.video_name = source["video_name"];
	        this.original_path = source["original_path"];
	        this.trash_path = source["trash_path"];
	        this.file_moved = source["file_moved"];
	        this.file_size = source["file_size"];
	        this.file_mod_time = source["file_mod_time"];
	        this.state = source["state"];
	        this.last_error = source["last_error"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }
	}

}

export namespace services {

	export class AIQualityDecisionMetrics {
	    decided: number;
	    approved: number;
	    rejected: number;
	    rate?: number;

	    static createFrom(source: any = {}) {
	        return new AIQualityDecisionMetrics(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.decided = source["decided"];
	        this.approved = source["approved"];
	        this.rejected = source["rejected"];
	        this.rate = source["rate"];
	    }
	}
	export class AIQualityFilter {
	    window: string;
	    tag_id: number;
	    confidence: string;
	    model_identifier: string;
	    prompt_schema_version: string;
	    comparison_prompt_version: string;
	    detection_version: string;

	    static createFrom(source: any = {}) {
	        return new AIQualityFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.window = source["window"];
	        this.tag_id = source["tag_id"];
	        this.confidence = source["confidence"];
	        this.model_identifier = source["model_identifier"];
	        this.prompt_schema_version = source["prompt_schema_version"];
	        this.comparison_prompt_version = source["comparison_prompt_version"];
	        this.detection_version = source["detection_version"];
	    }
	}
	export class AIQualityRunGroup {
	    model_identifier: string;
	    prompt_schema_version: string;
	    total: number;
	    completed: number;
	    skipped: number;
	    failed: number;
	    processing: number;
	    failure_rate?: number;
	    duration_p50_ms?: number;
	    duration_p95_ms?: number;
	    average_requests?: number;
	    average_tool_calls?: number;

	    static createFrom(source: any = {}) {
	        return new AIQualityRunGroup(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.model_identifier = source["model_identifier"];
	        this.prompt_schema_version = source["prompt_schema_version"];
	        this.total = source["total"];
	        this.completed = source["completed"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	        this.processing = source["processing"];
	        this.failure_rate = source["failure_rate"];
	        this.duration_p50_ms = source["duration_p50_ms"];
	        this.duration_p95_ms = source["duration_p95_ms"];
	        this.average_requests = source["average_requests"];
	        this.average_tool_calls = source["average_tool_calls"];
	    }
	}
	export class AIQualityRunMetrics {
	    total: number;
	    completed: number;
	    skipped: number;
	    failed: number;
	    processing: number;
	    failure_rate?: number;
	    duration_p50_ms?: number;
	    duration_p95_ms?: number;
	    average_requests?: number;
	    average_tool_calls?: number;

	    static createFrom(source: any = {}) {
	        return new AIQualityRunMetrics(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.completed = source["completed"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	        this.processing = source["processing"];
	        this.failure_rate = source["failure_rate"];
	        this.duration_p50_ms = source["duration_p50_ms"];
	        this.duration_p95_ms = source["duration_p95_ms"];
	        this.average_requests = source["average_requests"];
	        this.average_tool_calls = source["average_tool_calls"];
	    }
	}
	export class AIQualitySameSourceGroup {
	    confidence: string;
	    model_identifier: string;
	    comparison_prompt_version: string;
	    detection_version: string;
	    decided: number;
	    approved: number;
	    rejected: number;
	    rate?: number;

	    static createFrom(source: any = {}) {
	        return new AIQualitySameSourceGroup(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.confidence = source["confidence"];
	        this.model_identifier = source["model_identifier"];
	        this.comparison_prompt_version = source["comparison_prompt_version"];
	        this.detection_version = source["detection_version"];
	        this.decided = source["decided"];
	        this.approved = source["approved"];
	        this.rejected = source["rejected"];
	        this.rate = source["rate"];
	    }
	}
	export class AIQualityTagGroup {
	    tag_id: number;
	    tag_name: string;
	    confidence: string;
	    model_identifier: string;
	    prompt_schema_version: string;
	    decided: number;
	    approved: number;
	    rejected: number;
	    rate?: number;

	    static createFrom(source: any = {}) {
	        return new AIQualityTagGroup(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.tag_id = source["tag_id"];
	        this.tag_name = source["tag_name"];
	        this.confidence = source["confidence"];
	        this.model_identifier = source["model_identifier"];
	        this.prompt_schema_version = source["prompt_schema_version"];
	        this.decided = source["decided"];
	        this.approved = source["approved"];
	        this.rejected = source["rejected"];
	        this.rate = source["rate"];
	    }
	}
	export class AIQualityReport {
	    window: string;
	    from?: string;
	    generated_at: string;
	    tag_summary: AIQualityDecisionMetrics;
	    tag_groups: AIQualityTagGroup[];
	    same_source_summary: AIQualityDecisionMetrics;
	    same_source_groups: AIQualitySameSourceGroup[];
	    run_summary: AIQualityRunMetrics;
	    run_groups: AIQualityRunGroup[];

	    static createFrom(source: any = {}) {
	        return new AIQualityReport(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.window = source["window"];
	        this.from = source["from"];
	        this.generated_at = source["generated_at"];
	        this.tag_summary = this.convertValues(source["tag_summary"], AIQualityDecisionMetrics);
	        this.tag_groups = this.convertValues(source["tag_groups"], AIQualityTagGroup);
	        this.same_source_summary = this.convertValues(source["same_source_summary"], AIQualityDecisionMetrics);
	        this.same_source_groups = this.convertValues(source["same_source_groups"], AIQualitySameSourceGroup);
	        this.run_summary = this.convertValues(source["run_summary"], AIQualityRunMetrics);
	        this.run_groups = this.convertValues(source["run_groups"], AIQualityRunGroup);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AITagLibraryInput {
	    id: number;
	    namespace: string;
	    name: string;
	    color: string;
	    review_required: boolean;
	    is_active: boolean;

	    static createFrom(source: any = {}) {
	        return new AITagLibraryInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.namespace = source["namespace"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.review_required = source["review_required"];
	        this.is_active = source["is_active"];
	    }
	}
	export class AITaggingReviewItem {
	    id: number;
	    video_id: number;
	    video?: models.Video;
	    video_deleted: boolean;
	    suggested_name: string;
	    normalized_name: string;
	    matched_tag_id?: number;
	    matched_tag?: models.Tag;
	    confidence: string;
	    reasoning: string;
	    source_summary: string;
	    status: string;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new AITaggingReviewItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.video_id = source["video_id"];
	        this.video = this.convertValues(source["video"], models.Video);
	        this.video_deleted = source["video_deleted"];
	        this.suggested_name = source["suggested_name"];
	        this.normalized_name = source["normalized_name"];
	        this.matched_tag_id = source["matched_tag_id"];
	        this.matched_tag = this.convertValues(source["matched_tag"], models.Tag);
	        this.confidence = source["confidence"];
	        this.reasoning = source["reasoning"];
	        this.source_summary = source["source_summary"];
	        this.status = source["status"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AITaggingStatusSummary {
	    config_available: boolean;
	    pending: number;
	    same_source_unread: number;
	    processing: number;
	    completed: number;
	    skipped: number;
	    failed: number;

	    static createFrom(source: any = {}) {
	        return new AITaggingStatusSummary(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.config_available = source["config_available"];
	        this.pending = source["pending"];
	        this.same_source_unread = source["same_source_unread"];
	        this.processing = source["processing"];
	        this.completed = source["completed"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	    }
	}
	export class BatchVideoOperationError {
	    video_id: number;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new BatchVideoOperationError(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.error = source["error"];
	    }
	}
	export class BatchVideoOperationWarning {
	    video_id: number;
	    warning: string;

	    static createFrom(source: any = {}) {
	        return new BatchVideoOperationWarning(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.warning = source["warning"];
	    }
	}
	export class BatchVideoOperationResult {
	    requested: number;
	    succeeded: number;
	    failed: number;
	    errors: BatchVideoOperationError[];
	    warnings: BatchVideoOperationWarning[];

	    static createFrom(source: any = {}) {
	        return new BatchVideoOperationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requested = source["requested"];
	        this.succeeded = source["succeeded"];
	        this.failed = source["failed"];
	        this.errors = this.convertValues(source["errors"], BatchVideoOperationError);
	        this.warnings = this.convertValues(source["warnings"], BatchVideoOperationWarning);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class CleanupSameSourceGroup {
	    relation_id: number;
	    preferred: models.Video;
	    alternative: models.Video;
	    confidence: string;
	    reason: string;
	    estimated_savings: number;

	    static createFrom(source: any = {}) {
	        return new CleanupSameSourceGroup(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.relation_id = source["relation_id"];
	        this.preferred = this.convertValues(source["preferred"], models.Video);
	        this.alternative = this.convertValues(source["alternative"], models.Video);
	        this.confidence = source["confidence"];
	        this.reason = source["reason"];
	        this.estimated_savings = source["estimated_savings"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CleanupDuplicateGroup {
	    original: models.Video;
	    candidates: models.Video[];
	    reason: string;

	    static createFrom(source: any = {}) {
	        return new CleanupDuplicateGroup(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.original = this.convertValues(source["original"], models.Video);
	        this.candidates = this.convertValues(source["candidates"], models.Video);
	        this.reason = source["reason"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CleanupAnalysis {
	    duplicate_groups: CleanupDuplicateGroup[];
	    same_source_groups: CleanupSameSourceGroup[];
	    low_duration: models.Video[];
	    low_resolution: models.Video[];

	    static createFrom(source: any = {}) {
	        return new CleanupAnalysis(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.duplicate_groups = this.convertValues(source["duplicate_groups"], CleanupDuplicateGroup);
	        this.same_source_groups = this.convertValues(source["same_source_groups"], CleanupSameSourceGroup);
	        this.low_duration = this.convertValues(source["low_duration"], models.Video);
	        this.low_resolution = this.convertValues(source["low_resolution"], models.Video);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class CleanupProgress {
	    stage: string;
	    message: string;
	    current: number;
	    total: number;
	    path: string;

	    static createFrom(source: any = {}) {
	        return new CleanupProgress(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stage = source["stage"];
	        this.message = source["message"];
	        this.current = source["current"];
	        this.total = source["total"];
	        this.path = source["path"];
	    }
	}

	export class CleanupStatus {
	    running: boolean;
	    completed: boolean;
	    error: string;
	    progress: CleanupProgress;
	    analysis?: CleanupAnalysis;
	    started_at?: string;
	    updated_at?: string;

	    static createFrom(source: any = {}) {
	        return new CleanupStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.completed = source["completed"];
	        this.error = source["error"];
	        this.progress = this.convertValues(source["progress"], CleanupProgress);
	        this.analysis = this.convertValues(source["analysis"], CleanupAnalysis);
	        this.started_at = source["started_at"];
	        this.updated_at = source["updated_at"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CollectionVideoItem {
	    video: models.Video;
	    position: number;

	    static createFrom(source: any = {}) {
	        return new CollectionVideoItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video = this.convertValues(source["video"], models.Video);
	        this.position = source["position"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CollectionListItem {
	    collection: models.MediaCollection;
	    cover_url: string;
	    active_video_count: number;
	    cursor_name: string;

	    static createFrom(source: any = {}) {
	        return new CollectionListItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.collection = this.convertValues(source["collection"], models.MediaCollection);
	        this.cover_url = source["cover_url"];
	        this.active_video_count = source["active_video_count"];
	        this.cursor_name = source["cursor_name"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class CollectionDetail {
	    collection: CollectionListItem;
	    videos: CollectionVideoItem[];

	    static createFrom(source: any = {}) {
	        return new CollectionDetail(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.collection = this.convertValues(source["collection"], CollectionListItem);
	        this.videos = this.convertValues(source["videos"], CollectionVideoItem);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}


	export class ExternalSubtitleDetails {
	    path: string;
	    language: string;
	    segment_count: number;
	    last_segment_index: number;

	    static createFrom(source: any = {}) {
	        return new ExternalSubtitleDetails(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.language = source["language"];
	        this.segment_count = source["segment_count"];
	        this.last_segment_index = source["last_segment_index"];
	    }
	}
	export class FileMigrationResult {
	    video_id: number;
	    source: string;
	    destination: string;
	    warning?: string;

	    static createFrom(source: any = {}) {
	        return new FileMigrationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.source = source["source"];
	        this.destination = source["destination"];
	        this.warning = source["warning"];
	    }
	}
	export class FolderMigrationResult {
	    source: string;
	    destination: string;
	    videos_updated: number;
	    directories_updated: number;
	    warning?: string;

	    static createFrom(source: any = {}) {
	        return new FolderMigrationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.destination = source["destination"];
	        this.videos_updated = source["videos_updated"];
	        this.directories_updated = source["directories_updated"];
	        this.warning = source["warning"];
	    }
	}
	export class LibraryFilter {
	    search_mode: string;
	    keyword: string;
	    smart_view: string;
	    tag_ids: number[];
	    min_size: number;
	    max_size: number;
	    min_height: number;
	    max_height: number;
	    min_rating?: number;
	    max_rating?: number;
	    sort_mode: string;

	    static createFrom(source: any = {}) {
	        return new LibraryFilter(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.search_mode = source["search_mode"];
	        this.keyword = source["keyword"];
	        this.smart_view = source["smart_view"];
	        this.tag_ids = source["tag_ids"];
	        this.min_size = source["min_size"];
	        this.max_size = source["max_size"];
	        this.min_height = source["min_height"];
	        this.max_height = source["max_height"];
	        this.min_rating = source["min_rating"];
	        this.max_rating = source["max_rating"];
	        this.sort_mode = source["sort_mode"];
	    }
	}
	export class LibrarySubtitleHit {
	    video_id: number;
	    segment: subtitleparser.Segment;

	    static createFrom(source: any = {}) {
	        return new LibrarySubtitleHit(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.segment = this.convertValues(source["segment"], subtitleparser.Segment);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryVideoCursor {
	    sort_mode: string;
	    score: number;
	    size: number;
	    rating?: number;
	    rating_is_null: boolean;
	    id: number;

	    static createFrom(source: any = {}) {
	        return new LibraryVideoCursor(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sort_mode = source["sort_mode"];
	        this.score = source["score"];
	        this.size = source["size"];
	        this.rating = source["rating"];
	        this.rating_is_null = source["rating_is_null"];
	        this.id = source["id"];
	    }
	}
	export class LibraryVideoPage {
	    videos: models.Video[];
	    next_cursor?: LibraryVideoCursor;

	    static createFrom(source: any = {}) {
	        return new LibraryVideoPage(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.videos = this.convertValues(source["videos"], models.Video);
	        this.next_cursor = this.convertValues(source["next_cursor"], LibraryVideoCursor);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryVideoPageRequest {
	    filter: LibraryFilter;
	    cursor?: LibraryVideoCursor;
	    limit: number;

	    static createFrom(source: any = {}) {
	        return new LibraryVideoPageRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filter = this.convertValues(source["filter"], LibraryFilter);
	        this.cursor = this.convertValues(source["cursor"], LibraryVideoCursor);
	        this.limit = source["limit"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LibraryWatchRootStatus {
	    directory_id: number;
	    state: string;
	    reason_code: string;
	    message: string;
	    watch_count: number;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new LibraryWatchRootStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory_id = source["directory_id"];
	        this.state = source["state"];
	        this.reason_code = source["reason_code"];
	        this.message = source["message"];
	        this.watch_count = source["watch_count"];
	        this.updated_at = source["updated_at"];
	    }
	}
	export class LibraryWatcherStatus {
	    running: boolean;
	    roots: LibraryWatchRootStatus[];

	    static createFrom(source: any = {}) {
	        return new LibraryWatcherStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.roots = this.convertValues(source["roots"], LibraryWatchRootStatus);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataResolution {
	    normalized_name: string;
	    mode: string;
	    entity_id: number;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataResolution(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.normalized_name = source["normalized_name"];
	        this.mode = source["mode"];
	        this.entity_id = source["entity_id"];
	    }
	}
	export class LocalMetadataApplyRequest {
	    video_id: number;
	    manifest_sha256: string;
	    current_sha256: string;
	    selected_fields: string[];
	    overwrite_fields: string[];
	    people_resolutions: LocalMetadataResolution[];
	    collection_resolutions: LocalMetadataResolution[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataApplyRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.manifest_sha256 = source["manifest_sha256"];
	        this.current_sha256 = source["current_sha256"];
	        this.selected_fields = source["selected_fields"];
	        this.overwrite_fields = source["overwrite_fields"];
	        this.people_resolutions = this.convertValues(source["people_resolutions"], LocalMetadataResolution);
	        this.collection_resolutions = this.convertValues(source["collection_resolutions"], LocalMetadataResolution);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataApplyResult {
	    video_id: number;
	    manifest_sha256: string;
	    applied_fields: string[];
	    status: string;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataApplyResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.manifest_sha256 = source["manifest_sha256"];
	        this.applied_fields = source["applied_fields"];
	        this.status = source["status"];
	    }
	}
	export class LocalMetadataArtworkDiff {
	    field: string;
	    has_current: boolean;
	    source_name: string;
	    change_type: string;
	    default_selected: boolean;
	    requires_overwrite: boolean;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataArtworkDiff(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.field = source["field"];
	        this.has_current = source["has_current"];
	        this.source_name = source["source_name"];
	        this.change_type = source["change_type"];
	        this.default_selected = source["default_selected"];
	        this.requires_overwrite = source["requires_overwrite"];
	    }
	}
	export class LocalMetadataFailure {
	    video_id: number;
	    error_code: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataFailure(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.error_code = source["error_code"];
	        this.message = source["message"];
	    }
	}
	export class LocalMetadataBackfillStatus {
	    running: boolean;
	    cancelled: boolean;
	    completed: boolean;
	    total: number;
	    processed: number;
	    succeeded: number;
	    skipped: number;
	    failed: number;
	    current_video_id: number;
	    started_at?: string;
	    updated_at?: string;
	    failures: LocalMetadataFailure[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataBackfillStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.cancelled = source["cancelled"];
	        this.completed = source["completed"];
	        this.total = source["total"];
	        this.processed = source["processed"];
	        this.succeeded = source["succeeded"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	        this.current_video_id = source["current_video_id"];
	        this.started_at = source["started_at"];
	        this.updated_at = source["updated_at"];
	        this.failures = this.convertValues(source["failures"], LocalMetadataFailure);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataBatchApplyRequest {
	    requests: LocalMetadataApplyRequest[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataBatchApplyRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requests = this.convertValues(source["requests"], LocalMetadataApplyRequest);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataEntityCandidate {
	    source_name: string;
	    normalized_name: string;
	    matches: LocalMetadataEntity[];
	    default_mode: string;
	    default_entity_id: number;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataEntityCandidate(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source_name = source["source_name"];
	        this.normalized_name = source["normalized_name"];
	        this.matches = this.convertValues(source["matches"], LocalMetadataEntity);
	        this.default_mode = source["default_mode"];
	        this.default_entity_id = source["default_entity_id"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataEntity {
	    id: number;
	    name: string;
	    normalized_name: string;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataEntity(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.normalized_name = source["normalized_name"];
	    }
	}
	export class LocalMetadataRelationDiff {
	    field: string;
	    current: LocalMetadataEntity[];
	    source: LocalMetadataEntityCandidate[];
	    change_type: string;
	    default_selected: boolean;
	    requires_overwrite: boolean;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataRelationDiff(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.field = source["field"];
	        this.current = this.convertValues(source["current"], LocalMetadataEntity);
	        this.source = this.convertValues(source["source"], LocalMetadataEntityCandidate);
	        this.change_type = source["change_type"];
	        this.default_selected = source["default_selected"];
	        this.requires_overwrite = source["requires_overwrite"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataScalarDiff {
	    field: string;
	    current_value: string;
	    source_value: string;
	    change_type: string;
	    default_selected: boolean;
	    requires_overwrite: boolean;

	    static createFrom(source: any = {}) {
	        return new LocalMetadataScalarDiff(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.field = source["field"];
	        this.current_value = source["current_value"];
	        this.source_value = source["source_value"];
	        this.change_type = source["change_type"];
	        this.default_selected = source["default_selected"];
	        this.requires_overwrite = source["requires_overwrite"];
	    }
	}
	export class LocalMetadataDiff {
	    video_id: number;
	    manifest_sha256: string;
	    current_sha256: string;
	    status: string;
	    title: LocalMetadataScalarDiff;
	    original_title: LocalMetadataScalarDiff;
	    description: LocalMetadataScalarDiff;
	    people: LocalMetadataRelationDiff;
	    collection: LocalMetadataRelationDiff;
	    poster: LocalMetadataArtworkDiff;
	    fanart: LocalMetadataArtworkDiff;
	    warnings: string[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataDiff(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.manifest_sha256 = source["manifest_sha256"];
	        this.current_sha256 = source["current_sha256"];
	        this.status = source["status"];
	        this.title = this.convertValues(source["title"], LocalMetadataScalarDiff);
	        this.original_title = this.convertValues(source["original_title"], LocalMetadataScalarDiff);
	        this.description = this.convertValues(source["description"], LocalMetadataScalarDiff);
	        this.people = this.convertValues(source["people"], LocalMetadataRelationDiff);
	        this.collection = this.convertValues(source["collection"], LocalMetadataRelationDiff);
	        this.poster = this.convertValues(source["poster"], LocalMetadataArtworkDiff);
	        this.fanart = this.convertValues(source["fanart"], LocalMetadataArtworkDiff);
	        this.warnings = source["warnings"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataBatchPreview {
	    requested: number;
	    diffs: LocalMetadataDiff[];
	    failures: LocalMetadataFailure[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataBatchPreview(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requested = source["requested"];
	        this.diffs = this.convertValues(source["diffs"], LocalMetadataDiff);
	        this.failures = this.convertValues(source["failures"], LocalMetadataFailure);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class LocalMetadataBatchResult {
	    requested: number;
	    succeeded: number;
	    failed: number;
	    results: LocalMetadataApplyResult[];
	    failures: LocalMetadataFailure[];

	    static createFrom(source: any = {}) {
	        return new LocalMetadataBatchResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.requested = source["requested"];
	        this.succeeded = source["succeeded"];
	        this.failed = source["failed"];
	        this.results = this.convertValues(source["results"], LocalMetadataApplyResult);
	        this.failures = this.convertValues(source["failures"], LocalMetadataFailure);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}







	export class MergeTagsResult {
	    target_tag_id: number;
	    merged_tag_count: number;
	    video_links_moved: number;

	    static createFrom(source: any = {}) {
	        return new MergeTagsResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.target_tag_id = source["target_tag_id"];
	        this.merged_tag_count = source["merged_tag_count"];
	        this.video_links_moved = source["video_links_moved"];
	    }
	}
	export class PersonListItem {
	    person: models.Person;
	    avatar_url: string;
	    active_video_count: number;
	    cursor_name: string;

	    static createFrom(source: any = {}) {
	        return new PersonListItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.person = this.convertValues(source["person"], models.Person);
	        this.avatar_url = source["avatar_url"];
	        this.active_video_count = source["active_video_count"];
	        this.cursor_name = source["cursor_name"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PersonDetail {
	    person: PersonListItem;
	    videos: models.Video[];
	    next_video_id: number;

	    static createFrom(source: any = {}) {
	        return new PersonDetail(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.person = this.convertValues(source["person"], PersonListItem);
	        this.videos = this.convertValues(source["videos"], models.Video);
	        this.next_video_id = source["next_video_id"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class PlaybackReconcileResult {
	    video_id: number;
	    did_mark_stale: boolean;
	    did_relocate: boolean;
	    did_refresh_metadata: boolean;
	    needs_reload: boolean;
	    updated_video?: models.Video;
	    reason_code?: string;

	    static createFrom(source: any = {}) {
	        return new PlaybackReconcileResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.did_mark_stale = source["did_mark_stale"];
	        this.did_relocate = source["did_relocate"];
	        this.did_refresh_metadata = source["did_refresh_metadata"];
	        this.needs_reload = source["needs_reload"];
	        this.updated_video = this.convertValues(source["updated_video"], models.Video);
	        this.reason_code = source["reason_code"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PlaybackAttemptResult {
	    video?: models.Video;
	    dispatch_succeeded: boolean;
	    user_message?: string;
	    reason_code?: string;
	    selection_reason?: string;
	    reconcile_result?: PlaybackReconcileResult;

	    static createFrom(source: any = {}) {
	        return new PlaybackAttemptResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video = this.convertValues(source["video"], models.Video);
	        this.dispatch_succeeded = source["dispatch_succeeded"];
	        this.user_message = source["user_message"];
	        this.reason_code = source["reason_code"];
	        this.selection_reason = source["selection_reason"];
	        this.reconcile_result = this.convertValues(source["reconcile_result"], PlaybackReconcileResult);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class PreviewExternalAction {
	    action_id: string;
	    button_label: string;
	    hint: string;

	    static createFrom(source: any = {}) {
	        return new PreviewExternalAction(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.action_id = source["action_id"];
	        this.button_label = source["button_label"];
	        this.hint = source["hint"];
	    }
	}
	export class PreviewSourceDescriptor {
	    locator_strategy: string;
	    locator_value: string;
	    mime: string;

	    static createFrom(source: any = {}) {
	        return new PreviewSourceDescriptor(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.locator_strategy = source["locator_strategy"];
	        this.locator_value = source["locator_value"];
	        this.mime = source["mime"];
	    }
	}
	export class PreviewSession {
	    video_id: number;
	    mode: string;
	    display_name: string;
	    inline_source?: PreviewSourceDescriptor;
	    external_action?: PreviewExternalAction;
	    reason_code?: string;
	    reason_message?: string;

	    static createFrom(source: any = {}) {
	        return new PreviewSession(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.mode = source["mode"];
	        this.display_name = source["display_name"];
	        this.inline_source = this.convertValues(source["inline_source"], PreviewSourceDescriptor);
	        this.external_action = this.convertValues(source["external_action"], PreviewExternalAction);
	        this.reason_code = source["reason_code"];
	        this.reason_message = source["reason_message"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class RandomPlayRequest {
	    filter: LibraryFilter;
	    mode: string;
	    exclude_ids: number[];

	    static createFrom(source: any = {}) {
	        return new RandomPlayRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.filter = this.convertValues(source["filter"], LibraryFilter);
	        this.mode = source["mode"];
	        this.exclude_ids = source["exclude_ids"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SavedLibraryViewInput {
	    name: string;
	    search_mode: string;
	    keyword: string;
	    smart_view: string;
	    tag_ids: number[];
	    min_size: number;
	    max_size: number;
	    min_height: number;
	    max_height: number;
	    min_rating?: number;
	    max_rating?: number;
	    sort_mode: string;

	    static createFrom(source: any = {}) {
	        return new SavedLibraryViewInput(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.search_mode = source["search_mode"];
	        this.keyword = source["keyword"];
	        this.smart_view = source["smart_view"];
	        this.tag_ids = source["tag_ids"];
	        this.min_size = source["min_size"];
	        this.max_size = source["max_size"];
	        this.min_height = source["min_height"];
	        this.max_height = source["max_height"];
	        this.min_rating = source["min_rating"];
	        this.max_rating = source["max_rating"];
	        this.sort_mode = source["sort_mode"];
	    }
	}
	export class ScanSyncError {
	    operation: string;
	    directory?: string;
	    path?: string;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new ScanSyncError(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.operation = source["operation"];
	        this.directory = source["directory"];
	        this.path = source["path"];
	        this.error = source["error"];
	    }
	}
	export class ScanSyncResult {
	    directories: number;
	    scanned: number;
	    added: number;
	    deleted: number;
	    stale: number;
	    relocated: number;
	    metadata_refreshed: number;
	    skipped: number;
	    errors: ScanSyncError[];

	    static createFrom(source: any = {}) {
	        return new ScanSyncResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directories = source["directories"];
	        this.scanned = source["scanned"];
	        this.added = source["added"];
	        this.deleted = source["deleted"];
	        this.stale = source["stale"];
	        this.relocated = source["relocated"];
	        this.metadata_refreshed = source["metadata_refreshed"];
	        this.skipped = source["skipped"];
	        this.errors = this.convertValues(source["errors"], ScanSyncError);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ScannedFile {
	    path: string;
	    size: number;

	    static createFrom(source: any = {}) {
	        return new ScannedFile(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.size = source["size"];
	    }
	}
	export class ShortFeedServerStatus {
	    running: boolean;
	    bind_address: string;
	    port: number;
	    url: string;
	    lan_urls: string[];
	    startup_error: string;
	    fallback_used: boolean;
	    allowed_access: string;

	    static createFrom(source: any = {}) {
	        return new ShortFeedServerStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.bind_address = source["bind_address"];
	        this.port = source["port"];
	        this.url = source["url"];
	        this.lan_urls = source["lan_urls"];
	        this.startup_error = source["startup_error"];
	        this.fallback_used = source["fallback_used"];
	        this.allowed_access = source["allowed_access"];
	    }
	}
	export class SubtitleFingerprint {
	    size: number;
	    mod_time_ns: number;
	    sha256: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleFingerprint(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.size = source["size"];
	        this.mod_time_ns = source["mod_time_ns"];
	        this.sha256 = source["sha256"];
	    }
	}
	export class SubtitleEditDocument {
	    video_id: number;
	    fingerprint: SubtitleFingerprint;
	    entries: subtitleparser.EditorSegment[];

	    static createFrom(source: any = {}) {
	        return new SubtitleEditDocument(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.fingerprint = this.convertValues(source["fingerprint"], SubtitleFingerprint);
	        this.entries = this.convertValues(source["entries"], subtitleparser.EditorSegment);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleEngineStatus {
	    engine: string;
	    display_name: string;
	    supported: boolean;
	    available: boolean;
	    needs_prepare: boolean;
	    prepare_mode: string;
	    reason_code: string;
	    source_lang_mode: string;
	    reason_message: string;
	    prepare_hint: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleEngineStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.engine = source["engine"];
	        this.display_name = source["display_name"];
	        this.supported = source["supported"];
	        this.available = source["available"];
	        this.needs_prepare = source["needs_prepare"];
	        this.prepare_mode = source["prepare_mode"];
	        this.reason_code = source["reason_code"];
	        this.source_lang_mode = source["source_lang_mode"];
	        this.reason_message = source["reason_message"];
	        this.prepare_hint = source["prepare_hint"];
	    }
	}

	export class SubtitleGenerateRequest {
	    video_id: number;
	    video_name?: string;
	    engine: string;
	    source_lang: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleGenerateRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.video_name = source["video_name"];
	        this.engine = source["engine"];
	        this.source_lang = source["source_lang"];
	    }
	}
	export class SubtitleGenerateResult {
	    status: string;
	    video_id: number;
	    path?: string;
	    message?: string;
	    validation_code?: string;
	    force_eligible?: boolean;
	    engine?: string;
	    source_lang?: string;
	    warnings?: string[];
	    translation_status?: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleGenerateResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.video_id = source["video_id"];
	        this.path = source["path"];
	        this.message = source["message"];
	        this.validation_code = source["validation_code"];
	        this.force_eligible = source["force_eligible"];
	        this.engine = source["engine"];
	        this.source_lang = source["source_lang"];
	        this.warnings = source["warnings"];
	        this.translation_status = source["translation_status"];
	    }
	}
	export class SubtitleQueueTask {
	    task_id: number;
	    video_id: number;
	    video_name: string;
	    engine: string;
	    source_lang: string;
	    status: string;
	    position: number;
	    force_generate: boolean;
	    can_cancel: boolean;
	    enqueued_at: string;
	    started_at?: string;
	    finished_at?: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleQueueTask(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.task_id = source["task_id"];
	        this.video_id = source["video_id"];
	        this.video_name = source["video_name"];
	        this.engine = source["engine"];
	        this.source_lang = source["source_lang"];
	        this.status = source["status"];
	        this.position = source["position"];
	        this.force_generate = source["force_generate"];
	        this.can_cancel = source["can_cancel"];
	        this.enqueued_at = source["enqueued_at"];
	        this.started_at = source["started_at"];
	        this.finished_at = source["finished_at"];
	    }
	}
	export class SubtitleQueueSnapshot {
	    active_task?: SubtitleQueueTask;
	    queued_tasks: SubtitleQueueTask[];
	    total: number;

	    static createFrom(source: any = {}) {
	        return new SubtitleQueueSnapshot(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active_task = this.convertValues(source["active_task"], SubtitleQueueTask);
	        this.queued_tasks = this.convertValues(source["queued_tasks"], SubtitleQueueTask);
	        this.total = source["total"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

	export class SubtitleRetranslateEntry {
	    client_id: string;
	    text: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleRetranslateEntry(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.client_id = source["client_id"];
	        this.text = source["text"];
	    }
	}
	export class SubtitleRetranslateRequest {
	    video_id: number;
	    source_lang: string;
	    target_lang: string;
	    entries: SubtitleRetranslateEntry[];

	    static createFrom(source: any = {}) {
	        return new SubtitleRetranslateRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.source_lang = source["source_lang"];
	        this.target_lang = source["target_lang"];
	        this.entries = this.convertValues(source["entries"], SubtitleRetranslateEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleRetranslateResult {
	    entries: SubtitleRetranslateEntry[];

	    static createFrom(source: any = {}) {
	        return new SubtitleRetranslateResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entries = this.convertValues(source["entries"], SubtitleRetranslateEntry);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleSaveRequest {
	    video_id: number;
	    fingerprint: SubtitleFingerprint;
	    entries: subtitleparser.EditorSegment[];

	    static createFrom(source: any = {}) {
	        return new SubtitleSaveRequest(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.fingerprint = this.convertValues(source["fingerprint"], SubtitleFingerprint);
	        this.entries = this.convertValues(source["entries"], subtitleparser.EditorSegment);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleSaveResult {
	    status: string;
	    fingerprint?: SubtitleFingerprint;
	    issues?: subtitleparser.EditorValidationIssue[];
	    error_code?: string;
	    message?: string;

	    static createFrom(source: any = {}) {
	        return new SubtitleSaveResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.fingerprint = this.convertValues(source["fingerprint"], SubtitleFingerprint);
	        this.issues = this.convertValues(source["issues"], subtitleparser.EditorValidationIssue);
	        this.error_code = source["error_code"];
	        this.message = source["message"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleSearchMatch {
	    video: models.Video;
	    segment: subtitleparser.Segment;

	    static createFrom(source: any = {}) {
	        return new SubtitleSearchMatch(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video = this.convertValues(source["video"], models.Video);
	        this.segment = this.convertValues(source["segment"], subtitleparser.Segment);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class SubtitleValidationResult {
	    valid: boolean;
	    issues: subtitleparser.EditorValidationIssue[];

	    static createFrom(source: any = {}) {
	        return new SubtitleValidationResult(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.valid = source["valid"];
	        this.issues = this.convertValues(source["issues"], subtitleparser.EditorValidationIssue);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TechnicalBackfillFailure {
	    video_id: number;
	    name: string;
	    error: string;

	    static createFrom(source: any = {}) {
	        return new TechnicalBackfillFailure(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.name = source["name"];
	        this.error = source["error"];
	    }
	}
	export class TechnicalBackfillStatus {
	    running: boolean;
	    preparing: boolean;
	    cancelled: boolean;
	    completed: boolean;
	    total: number;
	    processed: number;
	    succeeded: number;
	    skipped: number;
	    failed: number;
	    current_video_id: number;
	    current_video_name: string;
	    started_at?: string;
	    updated_at?: string;
	    failures: TechnicalBackfillFailure[];

	    static createFrom(source: any = {}) {
	        return new TechnicalBackfillStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.preparing = source["preparing"];
	        this.cancelled = source["cancelled"];
	        this.completed = source["completed"];
	        this.total = source["total"];
	        this.processed = source["processed"];
	        this.succeeded = source["succeeded"];
	        this.skipped = source["skipped"];
	        this.failed = source["failed"];
	        this.current_video_id = source["current_video_id"];
	        this.current_video_name = source["current_video_name"];
	        this.started_at = source["started_at"];
	        this.updated_at = source["updated_at"];
	        this.failures = this.convertValues(source["failures"], TechnicalBackfillFailure);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TechnicalSnapshotStatus {
	    state: string;
	    is_stale: boolean;
	    has_error: boolean;

	    static createFrom(source: any = {}) {
	        return new TechnicalSnapshotStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.is_stale = source["is_stale"];
	        this.has_error = source["has_error"];
	    }
	}
	export class VideoArtworkData {
	    video_id: number;
	    kind: string;
	    mime: string;
	    data_url: string;

	    static createFrom(source: any = {}) {
	        return new VideoArtworkData(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.kind = source["kind"];
	        this.mime = source["mime"];
	        this.data_url = source["data_url"];
	    }
	}
	export class VideoArtworkStatus {
	    has_poster: boolean;
	    has_fanart: boolean;

	    static createFrom(source: any = {}) {
	        return new VideoArtworkStatus(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.has_poster = source["has_poster"];
	        this.has_fanart = source["has_fanart"];
	    }
	}
	export class VideoDetails {
	    video: models.Video;
	    effective_title: string;
	    people: PersonListItem[];
	    collections: CollectionListItem[];
	    technical_metadata?: models.VideoTechnicalMetadata;
	    streams: models.MediaStream[];
	    external_subtitle?: ExternalSubtitleDetails;
	    technical_status: TechnicalSnapshotStatus;
	    artwork: VideoArtworkStatus;

	    static createFrom(source: any = {}) {
	        return new VideoDetails(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video = this.convertValues(source["video"], models.Video);
	        this.effective_title = source["effective_title"];
	        this.people = this.convertValues(source["people"], PersonListItem);
	        this.collections = this.convertValues(source["collections"], CollectionListItem);
	        this.technical_metadata = this.convertValues(source["technical_metadata"], models.VideoTechnicalMetadata);
	        this.streams = this.convertValues(source["streams"], models.MediaStream);
	        this.external_subtitle = this.convertValues(source["external_subtitle"], ExternalSubtitleDetails);
	        this.technical_status = this.convertValues(source["technical_status"], TechnicalSnapshotStatus);
	        this.artwork = this.convertValues(source["artwork"], VideoArtworkStatus);
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class VideoDetailsUpdate {
	    video_id: number;
	    display_title: string;
	    original_title: string;
	    description: string;
	    personal_rating?: number;
	    person_ids: number[];
	    collection_ids: number[];

	    static createFrom(source: any = {}) {
	        return new VideoDetailsUpdate(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.video_id = source["video_id"];
	        this.display_title = source["display_title"];
	        this.original_title = source["original_title"];
	        this.description = source["description"];
	        this.personal_rating = source["personal_rating"];
	        this.person_ids = source["person_ids"];
	        this.collection_ids = source["collection_ids"];
	    }
	}
	export class VideoSameSourceReviewItem {
	    id: number;
	    video_a_id: number;
	    video_a?: models.Video;
	    video_a_deleted: boolean;
	    video_b_id: number;
	    video_b?: models.Video;
	    video_b_deleted: boolean;
	    status: string;
	    confidence: string;
	    reasoning: string;
	    is_unread: boolean;
	    created_at: string;
	    updated_at: string;

	    static createFrom(source: any = {}) {
	        return new VideoSameSourceReviewItem(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.video_a_id = source["video_a_id"];
	        this.video_a = this.convertValues(source["video_a"], models.Video);
	        this.video_a_deleted = source["video_a_deleted"];
	        this.video_b_id = source["video_b_id"];
	        this.video_b = this.convertValues(source["video_b"], models.Video);
	        this.video_b_deleted = source["video_b_deleted"];
	        this.status = source["status"];
	        this.confidence = source["confidence"];
	        this.reasoning = source["reasoning"];
	        this.is_unread = source["is_unread"];
	        this.created_at = source["created_at"];
	        this.updated_at = source["updated_at"];
	    }

		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace subtitleparser {

	export class EditorSegment {
	    index?: number;
	    client_id: string;
	    start_time_ms: number;
	    end_time_ms: number;
	    text: string;

	    static createFrom(source: any = {}) {
	        return new EditorSegment(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.client_id = source["client_id"];
	        this.start_time_ms = source["start_time_ms"];
	        this.end_time_ms = source["end_time_ms"];
	        this.text = source["text"];
	    }
	}
	export class EditorValidationIssue {
	    entry_index?: number;
	    client_id?: string;
	    code: string;
	    message: string;

	    static createFrom(source: any = {}) {
	        return new EditorValidationIssue(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.entry_index = source["entry_index"];
	        this.client_id = source["client_id"];
	        this.code = source["code"];
	        this.message = source["message"];
	    }
	}
	export class Segment {
	    index: number;
	    start_time_ms: number;
	    end_time_ms: number;
	    text: string;
	    lines: string[];

	    static createFrom(source: any = {}) {
	        return new Segment(source);
	    }

	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.start_time_ms = source["start_time_ms"];
	        this.end_time_ms = source["end_time_ms"];
	        this.text = source["text"];
	        this.lines = source["lines"];
	    }
	}

}
