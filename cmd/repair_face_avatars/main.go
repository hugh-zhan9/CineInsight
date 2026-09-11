// repair_face_avatars repairs avatars omitted by the former face naming flow.
// It never runs on startup: deliberately removing an avatar must remain effective.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func main() {
	env := flag.String("env", ".env", "PostgreSQL configuration file")
	dataDir := flag.String("data-dir", "", "application data directory containing faces/ and media-details/")
	apply := flag.Bool("apply", false, "copy missing avatars and update people; otherwise report only")
	flag.Parse()
	if *dataDir == "" {
		fatal("必须指定 --data-dir")
	}
	if err := godotenv.Load(*env); err != nil {
		fatal("无法读取配置文件")
	}
	dsn, err := database.PostgresDSNFromEnv()
	if err != nil {
		fatal("数据库配置无效")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fatal("数据库连接失败")
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatal("数据库句柄不可用")
	}
	defer sqlDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database.DB = db.WithContext(ctx)
	var candidates []struct {
		PersonID   uint   `json:"person_id"`
		ClusterID  uint   `json:"cluster_id"`
		AvatarPath string `json:"avatar_path"`
	}
	err = database.DB.Model(&models.FaceCluster{}).Select("people.id AS person_id, face_clusters.id AS cluster_id, people.avatar_path").Joins("JOIN people ON people.id = face_clusters.person_id").Where("face_clusters.status = ? AND people.avatar_path = ?", models.FaceClusterStatusNamed, "").Scan(&candidates).Error
	if err != nil {
		fatal("读取待修复人物失败")
	}
	fmt.Printf("缺少头像的已命名人脸簇：%d\n", len(candidates))
	if !*apply || len(candidates) == 0 {
		return
	}
	diagnostics := filepath.Join(*dataDir, "diagnostics")
	if err := os.MkdirAll(diagnostics, 0700); err != nil {
		fatal("无法创建修复快照目录")
	}
	snapshot, err := json.MarshalIndent(candidates, "", "  ")
	if err != nil {
		fatal("无法生成修复快照")
	}
	snapshotPath := filepath.Join(diagnostics, "face-avatar-before-"+time.Now().Format("20060102-150405.000000000")+".json")
	if err := os.WriteFile(snapshotPath, snapshot, 0600); err != nil {
		fatal("无法保存修复快照")
	}
	review := services.NewFaceReviewService(*dataDir, services.NewFaceAnalysisService(*dataDir, nil, nil))
	count, err := review.BackfillNamedFaceAvatars(ctx)
	fmt.Printf("已补回头像：%d\n", count)
	if err != nil {
		fatal("修复未全部完成；保留已完成结果与修复前快照，可排查后重试")
	}
}
func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
