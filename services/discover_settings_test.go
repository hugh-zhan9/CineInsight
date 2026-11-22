package services

import "testing"

func TestDiscoverVideoCountSettings(t *testing.T) {
	setupVideoServiceTestDB(t)
	s := &SettingsService{}
	current, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if current.DiscoverVideoCount != 12 {
		t.Fatalf("fresh discover count=%v", current.DiscoverVideoCount)
	}
	current.DiscoverVideoCount = 24
	if err := s.UpdateSettings(*current); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []int{-1, DiscoverVideoCountMax + 1} {
		current.DiscoverVideoCount = invalid
		if err := s.UpdateSettings(*current); err == nil {
			t.Fatalf("accepted count %d", invalid)
		}
	}
	current.DiscoverVideoCount = 0 // old UI does not send this setting
	if err := s.UpdateSettings(*current); err != nil {
		t.Fatal(err)
	}
	checked, err := s.GetSettings()
	if err != nil || checked.DiscoverVideoCount != 24 {
		t.Fatalf("persisted count: %+v, %v", checked, err)
	}
}
