package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"video-master/database"
	"video-master/models"
	"video-master/services"
)

func TestHealthSectionAllowsOnlyKnownSections(t *testing.T) {
	app := &App{}
	if _, err := app.GetSystemHealthSection("../secret"); err == nil {
		t.Fatal("unknown section was accepted")
	}
	for _, key := range healthSectionKeys {
		section, err := app.GetSystemHealthSection(key)
		if err != nil || section.Key != key || section.CheckedAt.IsZero() || section.Items == nil {
			t.Fatalf("%s: %+v %v", key, section, err)
		}
	}
	tasks, _ := app.GetSystemHealthSection("tasks")
	if tasks.Items[0].State != "unknown" {
		t.Fatal("an uninitialized registry must not be presented as healthy")
	}
}

func TestHealthRuntimeWithConstructedFaceServiceAndNilDatabase(t *testing.T) {
	previous := database.DB
	database.DB = nil
	t.Cleanup(func() { database.DB = previous })
	face := services.NewFaceRuntime(t.TempDir(), func() string {
		t.Fatal("diagnostics must not invoke the configuration/probe callback")
		return ""
	})
	app := &App{faceRuntime: face, settingsService: &services.SettingsService{}}
	section, err := app.GetSystemHealthSection("runtimes")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range section.Items {
		if item.Key == "face" {
			if item.State != "unknown" || item.ReasonCode != "not_checked" {
				t.Fatalf("%+v", item)
			}
			return
		}
	}
	t.Fatal("face runtime state is missing")
}

func TestHealthBackupReadFailureOmitsMetrics(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Migrator().DropTable(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	app := &App{backupService: services.NewBackupService(t.TempDir())}
	section, err := app.GetSystemHealthSection("database")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range section.Items {
		if item.Key == "backup" {
			if item.State != "unknown" || item.ReasonCode != "check_failed" || len(item.Metrics) != 0 {
				t.Fatalf("failed settings read became valid zeros: %+v", item)
			}
			return
		}
	}
	t.Fatal("backup state is missing")
}

func TestHealthReportWhitelistDropsPrivateProviderFields(t *testing.T) {
	secret := "PRIVATE_api-key-/Users/private/movie.srt?token=private"
	observed := time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)
	source := HealthSection{Key: "runtimes", Items: []HealthItem{{
		Key: secret, Label: secret, Detail: secret, LocalDetail: secret, ActionID: secret,
		State: secret, ReasonCode: secret, ObservedAt: &observed,
		Metrics: []HealthMetric{{Key: secret, Label: secret, Value: 2, Unit: secret}, {Key: "count", Label: secret, Value: 3, Unit: secret}},
	}}}
	content, err := json.Marshal(safeDiagnosticSection(source))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "PRIVATE") || strings.Contains(string(content), "private") {
		t.Fatalf("private fields escaped the whitelist: %s", content)
	}
	var result diagnosticSection
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatal(err)
	}
	if result.Items[0].State != "unknown" || result.Items[0].ReasonCode != "check_failed" || result.Items[0].Metrics["count"] != 3 || len(result.Items[0].Metrics) != 1 {
		t.Fatalf("unexpected safe projection: %+v", result)
	}
	if result.Items[0].ObservedAt == nil || !result.Items[0].ObservedAt.Equal(observed) {
		t.Fatal("cached check age was lost from report")
	}
}

func TestHealthStorageDisplaysLocalPathsButReportDoesNot(t *testing.T) {
	setupAppTestDB(t)
	privatePath := filepath.Join(t.TempDir(), "PRIVATE_LIBRARY_NAME")
	if err := database.DB.Create(&models.ScanDirectory{Path: privatePath, Alias: "PRIVATE_ALIAS"}).Error; err != nil {
		t.Fatal(err)
	}
	app := &App{directoryService: &services.DirectoryService{}}
	section, err := app.GetSystemHealthSection("storage")
	if err != nil || len(section.Items) != 1 {
		t.Fatalf("%+v %v", section, err)
	}
	if section.Items[0].LocalDetail != privatePath || section.Items[0].State != "unknown" {
		t.Fatalf("%+v", section)
	}
	content, _ := json.Marshal(safeDiagnosticSection(section))
	if strings.Contains(string(content), "PRIVATE") || strings.Contains(string(content), privatePath) {
		t.Fatalf("private root in report: %s", content)
	}
}

func TestHealthStorageReadFailureIsNotEmptySuccess(t *testing.T) {
	setupAppTestDB(t)
	if err := database.DB.Migrator().DropTable(&models.ScanDirectory{}); err != nil {
		t.Fatal(err)
	}
	section, err := (&App{directoryService: &services.DirectoryService{}}).GetSystemHealthSection("storage")
	if err != nil || section.Items[0].ReasonCode != "check_failed" || section.Items[0].State != "unknown" {
		t.Fatalf("%+v %v", section, err)
	}
}

func TestHealthReportSaveIsExclusiveAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "诊断报告.json")
	if err := writeHealthReport(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeHealthReport(path, []byte("second")); err == nil {
		t.Fatal("overwrote existing report")
	}
	content, _ := os.ReadFile(path)
	if string(content) != "first" {
		t.Fatalf("existing file changed: %s", content)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("report permissions: %o", info.Mode().Perm())
	}
	bad := filepath.Join(t.TempDir(), "missing", "report.json")
	if err := writeHealthReport(bad, []byte("test")); err == nil || strings.Contains(err.Error(), bad) {
		t.Fatalf("save failure missing or leaks path: %v", err)
	}
}

func TestHealthReportDoesNotFollowExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target"), filepath.Join(dir, "report")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := writeHealthReport(link, []byte("replacement")); err == nil {
		t.Fatal("followed existing symlink")
	}
	content, _ := os.ReadFile(target)
	if string(content) != "original" {
		t.Fatal("changed symlink target")
	}
}

func TestHealthExportCancelAndSave(t *testing.T) {
	old := selectHealthReportPath
	t.Cleanup(func() { selectHealthReportPath = old })
	app := &App{ctx: context.Background()}
	selectHealthReportPath = func(context.Context) (string, error) { return "", nil }
	result, err := app.ExportSystemHealthReport()
	if err != nil || result.Saved {
		t.Fatalf("cancel: %+v %v", result, err)
	}
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	selectHealthReportPath = func(context.Context) (string, error) { return path, nil }
	result, err = app.ExportSystemHealthReport()
	if err != nil || !result.Saved {
		t.Fatalf("save: %+v %v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report diagnosticReport
	if err := json.Unmarshal(content, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || len(report.Sections) != len(healthSectionKeys) {
		t.Fatalf("incomplete report: %+v", report)
	}
	if strings.Contains(string(content), path) || strings.Contains(string(content), "local_detail") || strings.Contains(string(content), "detail") {
		t.Fatalf("private display fields in export: %s", content)
	}
}
