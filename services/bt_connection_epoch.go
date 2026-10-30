package services

import (
	"gorm.io/gorm"
	"video-master/models"
)

// ClaimBTTaskEpoch must be called in the same transaction before inserting a new
// intent and its file rows. The caller must roll back that transaction on any
// subsequent failure. It fences configuration retirement across processes without
// row locks; restores and migration inserts must not call this runtime admission.
func ClaimBTTaskEpoch(tx *gorm.DB, instanceID string, revision, epoch uint64) error {
	if tx == nil || tx.Statement == nil {
		return ErrBTConnection
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrBTConnection
	}
	result := tx.Model(&models.BTConnection{}).Where("id = ? AND slot = ? AND revision = ? AND task_epoch = ? AND enabled = ?", instanceID, "local", revision, epoch, true).Update("task_epoch", gorm.Expr("task_epoch + 1"))
	if result.Error != nil {
		return ErrBTConnection
	}
	if result.RowsAffected != 1 {
		return ErrBTConflict
	}
	return nil
}
