package main

import (
	"context"
	"testing"
	"video-master/database"
	"video-master/internal/dbtest"
	"video-master/models"
	"video-master/services"
)

func TestCropDetectionDisabledStillAllowsManualAndStopFences(t *testing.T) {
	old := database.DB
	database.DB = dbtest.Open(t)
	t.Cleanup(func() { database.DB = old })
	database.DB.Create(&models.Settings{})
	slot := services.NewMediaWorkSlot()
	a := &App{cropDetection: services.NewCropDetectionService(services.NewVideoEditService(nil, slot, t.TempDir()), slot)}
	a.configureCropDetection(false)
	if _, e := a.StartCropDetection(); e != nil {
		t.Fatal("default off blocked manual", e)
	}
	a.configureCropDetection(false)
	if _, e := a.StartCropDetection(); e != nil {
		t.Fatal("disabled after run blocked manual", e)
	}
	a.stopCropDetection()
	if _, e := a.StartCropDetection(); e == nil {
		t.Fatal("maintenance/shutdown stop gate reopened")
	}
	if _, e := a.cropDetection.Start(context.Background()); e == nil {
		t.Fatal("service stop gate missing")
	}
}
