package services

import "sort"

// recapSlots 是「最常看的时段」的四段，顺序也是并列时的取舍顺序。
var recapSlots = []struct {
	key        string
	start, end int
}{
	{"late_night", 0, 6}, {"morning", 6, 12}, {"afternoon", 12, 18}, {"evening", 18, 24},
}

// habits 汇总分布与占比。days 是期内全部本地日期（含没有活动的日子），供日历热力与柱状图对齐坐标。
func (t *recapTally) habits(days []string) RecapHabits {
	total := t.plays + t.views
	h := RecapHabits{Total: total, Hours: t.hours[:], Days: make([]RecapDayBucket, 0, len(days)), PeakHour: -1,
		WeekendCount: t.weekend, LateNightCount: t.lateNight, Sources: []RecapSourceShare{}}
	for _, date := range days {
		bucket := RecapDayBucket{Date: date}
		if day := t.days[date]; day != nil {
			bucket = *day
		}
		h.Days = append(h.Days, bucket)
		if bucket.Plays+bucket.Views > 0 && (h.BusiestDay == nil || bucket.Plays+bucket.Views > h.BusiestDay.Plays+h.BusiestDay.Views) {
			busiest := bucket
			h.BusiestDay = &busiest
		}
	}
	if total == 0 {
		return h
	}
	h.WeekendShare = float64(t.weekend) / float64(total)
	h.LateNightShare = float64(t.lateNight) / float64(total)
	best := int64(0)
	for _, hour := range t.hours {
		if count := hour.Plays + hour.Views; count > best {
			best, h.PeakHour = count, hour.Hour
		}
	}
	best = 0
	for _, slot := range recapSlots {
		count := int64(0)
		for hour := slot.start; hour < slot.end; hour++ {
			count += t.hours[hour].Plays + t.hours[hour].Views
		}
		if count > best {
			best, h.PeakSlot = count, slot.key
		}
	}
	for share, count := range t.sources {
		share.Count = count
		h.Sources = append(h.Sources, share)
	}
	sort.Slice(h.Sources, func(i, j int) bool {
		a, b := h.Sources[i], h.Sources[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Kind != b.Kind {
			return a.Kind > b.Kind // video 在 image 前
		}
		return a.Source < b.Source
	})
	return h
}

// activeDays 返回有活动的天数与期内最长连续活跃天数（按本地日历日连续）。
func (t *recapTally) activeDays(days []string) (int, int) {
	active, longest, run := 0, 0, 0
	for _, date := range days {
		if day := t.days[date]; day != nil && day.Plays+day.Views > 0 {
			active++
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	return active, longest
}
