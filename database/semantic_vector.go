package database

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

var ErrSemanticVectorUnavailable = errors.New("semantic_vector_unavailable")

const MaxSemanticVectorDimensions = 16000

// SemanticVectorCapability describes whether pgvector-specific persistence is usable.
type SemanticVectorCapability struct {
	Available  bool   `json:"available"`
	Backend    string `json:"backend"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

// PrepareSemanticVectorStorage installs optional semantic tables without making startup depend on pgvector.
func PrepareSemanticVectorStorage(db *gorm.DB) SemanticVectorCapability {
	if db == nil {
		return SemanticVectorCapability{ReasonCode: "database_unavailable", Message: "数据库未初始化"}
	}
	if err := db.AutoMigrate(&models.SemanticIndexProfile{}, &models.VideoSemanticIndex{}, &models.SemanticIndexAttempt{}); err != nil {
		return SemanticVectorCapability{Backend: db.Dialector.Name(), ReasonCode: "metadata_migration_failed", Message: boundedSemanticVectorMessage(err)}
	}
	backend := db.Dialector.Name()
	if backend != "postgres" {
		return SemanticVectorCapability{Backend: backend, ReasonCode: "pgvector_requires_postgres", Message: "语义向量检索需要 PostgreSQL pgvector"}
	}
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		return SemanticVectorCapability{Backend: backend, ReasonCode: "extension_unavailable", Message: boundedSemanticVectorMessage(err)}
	}
	const vectorTableDDL = `
CREATE TABLE IF NOT EXISTS video_semantic_vectors (
    video_id BIGINT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    model_identifier TEXT NOT NULL,
    dimension INTEGER NOT NULL CHECK (dimension > 0),
    generation INTEGER NOT NULL DEFAULT 1,
    embedding vector NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (video_id, model_identifier, dimension),
    CHECK (vector_dims(embedding) = dimension)
)`
	if err := db.Exec(vectorTableDDL).Error; err != nil {
		return SemanticVectorCapability{Backend: backend, ReasonCode: "vector_table_unavailable", Message: boundedSemanticVectorMessage(err)}
	}
	if err := db.Exec("ALTER TABLE video_semantic_vectors ADD COLUMN IF NOT EXISTS generation INTEGER NOT NULL DEFAULT 1").Error; err != nil {
		return SemanticVectorCapability{Backend: backend, ReasonCode: "vector_table_unavailable", Message: boundedSemanticVectorMessage(err)}
	}
	return SemanticVectorCapability{Available: true, Backend: backend}
}

// EnsureSemanticVectorANNIndex types the embedding column to the active
// dimension and creates the HNSW cosine index so nearest-neighbour queries can
// leave sequential scans. It runs only once the dimension is known. Vectors
// persisted under other dimensions belong to abandoned profiles (a rebuild is
// already required to use them) and are regenerable, so they are removed
// before the column is typed.
func EnsureSemanticVectorANNIndex(db *gorm.DB, capability SemanticVectorCapability, dimension int) error {
	if db == nil || !capability.Available || capability.Backend != "postgres" {
		return ErrSemanticVectorUnavailable
	}
	if dimension <= 0 {
		return fmt.Errorf("invalid semantic vector dimension %d", dimension)
	}
	var typmod int
	if err := db.Raw(`SELECT atttypmod FROM pg_attribute WHERE attrelid = 'video_semantic_vectors'::regclass AND attname = 'embedding'`).Scan(&typmod).Error; err != nil {
		return err
	}
	if typmod != dimension {
		if err := db.Exec(`DELETE FROM video_semantic_vectors WHERE dimension <> ?`, dimension).Error; err != nil {
			return err
		}
		alter := fmt.Sprintf(`ALTER TABLE video_semantic_vectors ALTER COLUMN embedding TYPE vector(%d) USING embedding::vector(%d)`, dimension, dimension)
		if err := db.Exec(alter).Error; err != nil {
			return err
		}
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_video_semantic_vectors_embedding_hnsw ON video_semantic_vectors USING hnsw (embedding vector_cosine_ops)`).Error
}

// UpsertSemanticVector writes only to the optional PostgreSQL pgvector table.
func UpsertSemanticVector(db *gorm.DB, capability SemanticVectorCapability, videoID uint, model string, dimension, generation int, embedding []float64, now time.Time) error {
	if db == nil || !capability.Available || capability.Backend != "postgres" {
		return ErrSemanticVectorUnavailable
	}
	if videoID == 0 || strings.TrimSpace(model) == "" || dimension <= 0 || generation <= 0 || len(embedding) != dimension {
		return fmt.Errorf("invalid semantic vector metadata")
	}
	literal, err := SemanticVectorLiteral(embedding)
	if err != nil {
		return err
	}
	return db.Exec(`
INSERT INTO video_semantic_vectors (video_id, model_identifier, dimension, generation, embedding, updated_at)
VALUES (?, ?, ?, ?, CAST(? AS vector), ?)
ON CONFLICT (video_id, model_identifier, dimension)
DO UPDATE SET generation = EXCLUDED.generation, embedding = EXCLUDED.embedding, updated_at = EXCLUDED.updated_at`,
		videoID, model, dimension, generation, literal, now).Error
}

// SemanticVectorLiteral validates and formats a vector for a parameterized pgvector cast.
func SemanticVectorLiteral(values []float64) (string, error) {
	if len(values) == 0 || len(values) > MaxSemanticVectorDimensions {
		return "", fmt.Errorf("semantic vector dimension %d is unsupported", len(values))
	}
	var builder strings.Builder
	norm := 0.0
	builder.WriteByte('[')
	for index, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", fmt.Errorf("semantic vector contains a non-finite value")
		}
		norm += value * value
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	}
	builder.WriteByte(']')
	if norm == 0 {
		return "", fmt.Errorf("semantic vector cannot be all zero")
	}
	return builder.String(), nil
}

func boundedSemanticVectorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
