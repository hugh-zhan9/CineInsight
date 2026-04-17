export namespace gorm {
	
	export class DeletedAt {
	    // Go type: time
	    Time: any;
	    Valid: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeletedAt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Time = this.convertValues(source["Time"], null);
	        this.Valid = source["Valid"];
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

export namespace models {
	
	export class ScanDirectory {
	    id: number;
	    path: string;
	    alias: string;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new ScanDirectory(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.path = source["path"];
	        this.alias = source["alias"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
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
	export class Settings {
	    id: number;
	    confirm_before_delete: boolean;
	    delete_original_file: boolean;
	    video_extensions: string;
	    play_weight: number;
	    auto_scan_on_startup: boolean;
	    theme: string;
	    log_enabled: boolean;
	    bilingual_enabled: boolean;
	    bilingual_lang: string;
	    deepl_api_key: string;
	    // Go type: time
	    updated_at: any;
	
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
	        this.theme = source["theme"];
	        this.log_enabled = source["log_enabled"];
	        this.bilingual_enabled = source["bilingual_enabled"];
	        this.bilingual_lang = source["bilingual_lang"];
	        this.deepl_api_key = source["deepl_api_key"];
	        this.updated_at = this.convertValues(source["updated_at"], null);
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
	export class Tag {
	    id: number;
	    name: string;
	    color: string;
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Tag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.color = source["color"];
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
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
	export class Video {
	    id: number;
	    name: string;
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
	    // Go type: time
	    last_played_at?: any;
	    tags: Tag[];
	    // Go type: time
	    created_at: any;
	    // Go type: time
	    updated_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Video(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
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
	        this.last_played_at = this.convertValues(source["last_played_at"], null);
	        this.tags = this.convertValues(source["tags"], Tag);
	        this.created_at = this.convertValues(source["created_at"], null);
	        this.updated_at = this.convertValues(source["updated_at"], null);
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

export namespace services {
	
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
	    low_duration: models.Video[];
	    low_resolution: models.Video[];
	
	    static createFrom(source: any = {}) {
	        return new CleanupAnalysis(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.duplicate_groups = this.convertValues(source["duplicate_groups"], CleanupDuplicateGroup);
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

	export class PlaybackReconcileResult {
	    video_id: number;
	    did_mark_stale: boolean;
	    did_relocate: boolean;
	    did_refresh_metadata: boolean;
	    needs_reload: boolean;
	    updated_video?: models.Video;
	    reason_code: string;

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
	    user_message: string;
	    reason_code: string;
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

	export class PreviewSession {
	    video_id: number;
	    mode: string;
	    display_name: string;
	    inline_source?: PreviewSourceDescriptor;
	    external_action?: PreviewExternalAction;
	    reason_code: string;
	    reason_message: string;

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

}

export namespace subtitleparser {
	
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
