use cine_db::{
    legacy_schema_status, load_pg_config_from_env, LegacySchemaSnapshot,
    OPTIONAL_LEGACY_EXTENSIONS, OPTIONAL_LEGACY_INDEXES, REQUIRED_LEGACY_INDEXES,
    REQUIRED_LEGACY_TABLES,
};

#[test]
fn loads_pg_config_with_legacy_env_defaults() {
    std::env::set_var("PG_HOST", "localhost");
    std::env::set_var("PG_USER", "cine");
    std::env::set_var("PG_DB", "cineinsight");
    std::env::remove_var("PG_PORT");
    std::env::remove_var("PG_SSLMODE");
    std::env::remove_var("PG_PASSWORD");
    std::env::remove_var("PG_TIMEZONE");

    let config = load_pg_config_from_env().expect("config should load");

    assert_eq!(config.host, "localhost");
    assert_eq!(config.user, "cine");
    assert_eq!(config.database, "cineinsight");
    assert_eq!(config.port, 5432);
    assert_eq!(config.sslmode, "disable");
    assert!(config.password.is_none());
    assert!(config.timezone.is_none());
}

#[test]
fn reports_missing_required_legacy_tables_and_indexes() {
    let snapshot = LegacySchemaSnapshot {
        tables: vec!["videos".into(), "tags".into()],
        indexes: vec!["idx_videos_path_active".into()],
        extensions: vec![],
    };

    let status = legacy_schema_status(&snapshot);

    assert_eq!(status.required_tables, REQUIRED_LEGACY_TABLES);
    assert!(status.missing_tables.contains(&"settings".to_string()));
    assert!(status
        .missing_indexes
        .contains(&"idx_video_tags_video_tag".to_string()));
    assert!(status.missing_extensions.is_empty());
    assert!(status
        .missing_optional_extensions
        .contains(&"pg_trgm".to_string()));
    assert!(!status.compatible);
}

#[test]
fn accepts_complete_static_legacy_schema_snapshot() {
    let snapshot = LegacySchemaSnapshot {
        tables: REQUIRED_LEGACY_TABLES
            .iter()
            .map(|value| value.to_string())
            .collect(),
        indexes: REQUIRED_LEGACY_INDEXES
            .iter()
            .chain(OPTIONAL_LEGACY_INDEXES.iter())
            .map(|value| value.to_string())
            .collect(),
        extensions: OPTIONAL_LEGACY_EXTENSIONS
            .iter()
            .map(|value| value.to_string())
            .collect(),
    };

    let status = legacy_schema_status(&snapshot);

    assert!(status.compatible);
    assert!(status.missing_tables.is_empty());
    assert!(status.missing_indexes.is_empty());
    assert!(status.missing_extensions.is_empty());
    assert!(status.missing_optional_indexes.is_empty());
    assert!(status.missing_optional_extensions.is_empty());
}

#[test]
fn accepts_legacy_schema_without_optional_trigram_acceleration() {
    let snapshot = LegacySchemaSnapshot {
        tables: REQUIRED_LEGACY_TABLES
            .iter()
            .map(|value| value.to_string())
            .collect(),
        indexes: REQUIRED_LEGACY_INDEXES
            .iter()
            .map(|value| value.to_string())
            .collect(),
        extensions: vec![],
    };

    let status = legacy_schema_status(&snapshot);

    assert!(status.compatible);
    assert!(status.missing_tables.is_empty());
    assert!(status.missing_indexes.is_empty());
    assert!(status.missing_extensions.is_empty());
    assert_eq!(status.optional_indexes, OPTIONAL_LEGACY_INDEXES);
    assert_eq!(status.optional_extensions, OPTIONAL_LEGACY_EXTENSIONS);
    assert!(status
        .missing_optional_indexes
        .contains(&"idx_subtitle_segments_text_trgm".to_string()));
    assert!(status
        .missing_optional_extensions
        .contains(&"pg_trgm".to_string()));
}
