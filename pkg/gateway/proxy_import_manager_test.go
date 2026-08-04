package gateway

import (
	"testing"
	"time"

	"nvidia-api-gateway/pkg/models"
)

func TestNormalizeProxyImportScheduleTimes(t *testing.T) {
	items, err := normalizeProxyImportScheduleTimes([]string{"9:00, 18:30", "09:00", "23:15"})
	if err != nil {
		t.Fatalf("normalizeProxyImportScheduleTimes error: %v", err)
	}
	want := []string{"09:00", "18:30", "23:15"}
	if len(items) != len(want) {
		t.Fatalf("len(items) = %d, want %d (%v)", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("items[%d] = %q, want %q", i, items[i], want[i])
		}
	}
}

func TestNormalizeProxyImportScheduleTimesRejectsInvalidClock(t *testing.T) {
	if _, err := normalizeProxyImportScheduleTimes([]string{"25:99"}); err == nil {
		t.Fatal("expected invalid time error")
	}
}

func TestShouldTriggerProxyImportSchedule(t *testing.T) {
	now := time.Date(2026, 5, 7, 9, 0, 10, 0, time.Local)
	schedule := defaultProxyImportScheduleForTest()
	schedule.Enabled = true
	schedule.Times = []string{"09:00", "18:30"}
	if !shouldTriggerProxyImportSchedule(schedule, now) {
		t.Fatal("expected schedule to trigger at 09:00")
	}
	schedule.LastRunAt = time.Date(2026, 5, 7, 9, 0, 1, 0, time.Local)
	if shouldTriggerProxyImportSchedule(schedule, now) {
		t.Fatal("expected duplicate minute trigger to be skipped")
	}
}

func TestComputeNextProxyImportRun(t *testing.T) {
	now := time.Date(2026, 5, 7, 9, 5, 0, 0, time.Local)
	schedule := defaultProxyImportScheduleForTest()
	schedule.Enabled = true
	schedule.Times = []string{"09:00", "18:30"}
	next := computeNextProxyImportRun(schedule, now)
	if next == nil {
		t.Fatal("expected next run")
	}
	if next.Hour() != 18 || next.Minute() != 30 {
		t.Fatalf("next = %v, want same day 18:30", next)
	}
}

func defaultProxyImportScheduleForTest() models.ProxyImportSchedule {
	return models.ProxyImportSchedule{
		Enabled:        false,
		Times:          []string{},
		Mode:           "all",
		Group:          "自动抓取",
		Limit:          800,
		Concurrency:    96,
		TimeoutSeconds: 4,
	}
}
