package services

import "testing"

func TestPerformanceLoggingPreferenceIsIndependentAndPersisted(t *testing.T) {
	setupVideoServiceTestDB(t)
	s := &SettingsService{}
	current, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !current.PerformanceLogsEnabled() {
		t.Fatal("performance logs should default on")
	}
	disabled, enabled := false, true
	current.PerfLogEnabled = &disabled
	current.LogEnabled = true
	if err := s.UpdateSettings(*current); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.PerfLogEnabled == nil || got.PerformanceLogsEnabled() || !got.LogEnabled {
		t.Fatal("turning off performance logs changed system logs or did not persist")
	}
	got.PerfLogEnabled = nil // An older client omits the newer field.
	got.LogEnabled = false
	if err := s.UpdateSettings(*got); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.PerfLogEnabled == nil || got.PerformanceLogsEnabled() || got.LogEnabled {
		t.Fatal("omission changed the opt-out or system logs did not save")
	}
	got.PerfLogEnabled = &enabled
	if err := s.UpdateSettings(*got); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !got.PerformanceLogsEnabled() || got.LogEnabled {
		t.Fatal("turning on performance logs also enabled system logs")
	}
}
