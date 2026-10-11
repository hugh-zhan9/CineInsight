package services

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRecapInvalidPeriod 是期次参数非法（未知 period、年份越界、月份越界）。
var ErrRecapInvalidPeriod = errors.New("recap_invalid_period")

// recapWindow 是一期回顾的时间窗，全部按本机时区的自然边界取 [start, end)。
// prevStart/prevEnd 是对比用的上一同期：进行中的期次只取上一期同样长的一段。
type recapWindow struct {
	period     string
	year       int
	month      int
	label      string
	start      time.Time
	end        time.Time
	inProgress bool
	prevLabel  string
	prevStart  time.Time
	prevEnd    time.Time
}

func resolveRecapWindow(req RecapRequest, now time.Time, loc *time.Location) (recapWindow, error) {
	now = now.In(loc)
	period := strings.TrimSpace(req.Period)
	var w recapWindow
	switch period {
	case RecapPeriodLive:
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		w = recapWindow{period: period, year: now.Year(), month: int(now.Month()), label: "今天",
			start: start, end: start.AddDate(0, 0, 1), prevLabel: "昨天同一时段", prevStart: start.AddDate(0, 0, -1)}
	case RecapPeriodMonth:
		if req.Year < 1 || req.Year > 9999 || req.Month < 1 || req.Month > 12 {
			return recapWindow{}, fmt.Errorf("%w: %d-%d", ErrRecapInvalidPeriod, req.Year, req.Month)
		}
		start := time.Date(req.Year, time.Month(req.Month), 1, 0, 0, 0, 0, loc)
		w = recapWindow{period: period, year: req.Year, month: req.Month, label: fmt.Sprintf("%d年%d月", req.Year, req.Month),
			start: start, end: start.AddDate(0, 1, 0), prevLabel: "上月", prevStart: start.AddDate(0, -1, 0)}
	case RecapPeriodYear:
		if req.Year < 1 || req.Year > 9999 {
			return recapWindow{}, fmt.Errorf("%w: %d", ErrRecapInvalidPeriod, req.Year)
		}
		start := time.Date(req.Year, time.January, 1, 0, 0, 0, 0, loc)
		w = recapWindow{period: period, year: req.Year, label: fmt.Sprintf("%d年", req.Year),
			start: start, end: start.AddDate(1, 0, 0), prevLabel: "去年", prevStart: start.AddDate(-1, 0, 0)}
	default:
		return recapWindow{}, fmt.Errorf("%w: %q", ErrRecapInvalidPeriod, req.Period)
	}
	// 上一期的自然终点就是本期起点；本期进行中时只取上一期里同样长的一段，「同期」才可比。
	w.prevEnd = w.start
	if !now.Before(w.start) && now.Before(w.end) {
		w.inProgress = true
		if cut := w.prevStart.Add(now.Sub(w.start)); cut.Before(w.prevEnd) {
			w.prevEnd = cut
		}
		if period != RecapPeriodLive {
			w.prevLabel += "同期"
		}
	}
	return w, nil
}

// recapDays 列出 [start, end) 内的每个本地日期。
func recapDays(start, end time.Time) []string {
	days := []string{}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day.Format("2006-01-02"))
	}
	return days
}
