package database

import (
	"fmt"
	"strings"
	"time"
	"video-master/models"

	"gorm.io/gorm"
)

// PrepareImageSemanticVectorStorage 安装可选的图片语义表，启动不依赖 pgvector，镜像视频侧。
func PrepareImageSemanticVectorStorage(db *gorm.DB) SemanticVectorCapability {
	if db == nil {
		return SemanticVectorCapability{ReasonCode: "database_unavailable", Message: "数据库未初始化"}
	}
	if err := db.AutoMigrate(&models.SemanticIndexProfile{}, &models.ImageSemanticIndex{}, &models.ImageSemanticIndexAttempt{}); err != nil {
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
CREATE TABLE IF NOT EXISTS image_semantic_vectors (
    image_id BIGINT NOT NULL REFERENCES images(id) ON DELETE CASCADE,
    model_identifier TEXT NOT NULL,
    dimension INTEGER NOT NULL CHECK (dimension > 0),
    generation INTEGER NOT NULL DEFAULT 1,
    embedding vector NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (image_id, model_identifier, dimension),
    CHECK (vector_dims(embedding) = dimension)
)`
	if err := db.Exec(vectorTableDDL).Error; err != nil {
		return SemanticVectorCapability{Backend: backend, ReasonCode: "vector_table_unavailable", Message: boundedSemanticVectorMessage(err)}
	}
	return SemanticVectorCapability{Available: true, Backend: backend}
}

// EnsureImageSemanticVectorANNIndex 将 embedding 列定型到当前激活维度并创建 HNSW 余弦索引，
// 使最近邻查询摆脱顺序扫描；仅在维度已知后执行。其他维度下持久化的向量属于已废弃的
// profile（使用它们本就需要 rebuild）且可再生，故在定型前先移除，逻辑镜像视频侧。
func EnsureImageSemanticVectorANNIndex(db *gorm.DB, capability SemanticVectorCapability, dimension int) error {
	if db == nil || !capability.Available || capability.Backend != "postgres" {
		return ErrSemanticVectorUnavailable
	}
	if dimension <= 0 {
		return fmt.Errorf("invalid semantic vector dimension %d", dimension)
	}
	var typmod int
	if err := db.Raw(`SELECT atttypmod FROM pg_attribute WHERE attrelid = 'image_semantic_vectors'::regclass AND attname = 'embedding'`).Scan(&typmod).Error; err != nil {
		return err
	}
	if typmod != dimension {
		if err := db.Exec(`DELETE FROM image_semantic_vectors WHERE dimension <> ?`, dimension).Error; err != nil {
			return err
		}
		alter := fmt.Sprintf(`ALTER TABLE image_semantic_vectors ALTER COLUMN embedding TYPE vector(%d) USING embedding::vector(%d)`, dimension, dimension)
		if err := db.Exec(alter).Error; err != nil {
			return err
		}
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_image_semantic_vectors_embedding_hnsw ON image_semantic_vectors USING hnsw (embedding vector_cosine_ops)`).Error
}

// UpsertImageSemanticVector 仅写入可选的 PostgreSQL pgvector 图片向量表，镜像视频侧。
func UpsertImageSemanticVector(db *gorm.DB, capability SemanticVectorCapability, imageID uint, model string, dimension, generation int, embedding []float64, now time.Time) error {
	if db == nil || !capability.Available || capability.Backend != "postgres" {
		return ErrSemanticVectorUnavailable
	}
	if imageID == 0 || strings.TrimSpace(model) == "" || dimension <= 0 || generation <= 0 || len(embedding) != dimension {
		return fmt.Errorf("invalid semantic vector metadata")
	}
	literal, err := SemanticVectorLiteral(embedding)
	if err != nil {
		return err
	}
	return db.Exec(`
INSERT INTO image_semantic_vectors (image_id, model_identifier, dimension, generation, embedding, updated_at)
VALUES (?, ?, ?, ?, CAST(? AS vector), ?)
ON CONFLICT (image_id, model_identifier, dimension)
DO UPDATE SET generation = EXCLUDED.generation, embedding = EXCLUDED.embedding, updated_at = EXCLUDED.updated_at`,
		imageID, model, dimension, generation, literal, now).Error
}
