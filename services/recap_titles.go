package services

import (
	"fmt"
	"math"
	"sort"
)

// 称号规则（D-RECAP-CONTENT）：全部由事实按固定阈值给出，阈值含边界（≥）；占比类称号要求分母
// 至少 recapTitleMinSample 条，避免一两条记录就封号。最多给三个，按超出阈值的倍数排序，
// 再按规则顺序。一条都不满足时给「记录者」，只陈述条数，不编造偏好。空期不出称号。
const recapTitleMinSample = 3

type recapTitleFacts struct {
	plays, views, lateNight, weekend int64
	newVideos, rewatched             int64
	longestStreak                    int
	topTagName                       string
	topTagWeight                     int64
}

type recapTitleCandidate struct {
	title    RecapTitle
	strength float64
	order    int
}

func recapPercent(part, whole int64) int64 {
	return int64(math.Round(float64(part) * 100 / float64(whole)))
}

func recapTitles(f recapTitleFacts) []RecapTitle {
	total := f.plays + f.views
	if total == 0 {
		return []RecapTitle{}
	}
	var picked []recapTitleCandidate
	add := func(order int, key, name, detail string, value, threshold float64) {
		picked = append(picked, recapTitleCandidate{RecapTitle{Key: key, Name: name, Detail: detail}, value / threshold, order})
	}
	if total >= recapTitleMinSample && f.lateNight*100 >= 30*total {
		add(0, "night_owl", "夜猫子", fmt.Sprintf("%d%% 的记录发生在 0–5 点（%d/%d）", recapPercent(f.lateNight, total), f.lateNight, total), float64(f.lateNight)/float64(total), 0.30)
	}
	if total >= recapTitleMinSample && f.weekend*100 >= 60*total {
		add(1, "weekend_fan", "周末影迷", fmt.Sprintf("%d%% 的记录在周末（%d/%d）", recapPercent(f.weekend, total), f.weekend, total), float64(f.weekend)/float64(total), 0.60)
	}
	classified := f.newVideos + f.rewatched
	if classified >= recapTitleMinSample && f.rewatched*100 >= 50*classified {
		add(2, "rewatcher", "重温派", fmt.Sprintf("重温 %d 部，占新旧合计的 %d%%", f.rewatched, recapPercent(f.rewatched, classified)), float64(f.rewatched)/float64(classified), 0.50)
	}
	if classified >= recapTitleMinSample && f.newVideos*100 >= 50*classified {
		add(3, "explorer", "探索家", fmt.Sprintf("新发现 %d 部，占新旧合计的 %d%%", f.newVideos, recapPercent(f.newVideos, classified)), float64(f.newVideos)/float64(classified), 0.50)
	}
	if f.longestStreak >= 7 {
		add(4, "streak", "连续打卡", fmt.Sprintf("最长连续 %d 天都有记录", f.longestStreak), float64(f.longestStreak), 7)
	}
	if f.plays >= recapTitleMinSample && f.topTagName != "" && f.topTagWeight*100 >= 40*f.plays {
		add(5, "tag_loyal", "标签专一", fmt.Sprintf("%d%% 的播放带着「%s」", recapPercent(f.topTagWeight, f.plays), f.topTagName), float64(f.topTagWeight)/float64(f.plays), 0.40)
	}
	if f.views > 0 && f.views >= f.plays {
		add(6, "image_lover", "图片控", fmt.Sprintf("看图 %d 次，播放 %d 次", f.views, f.plays), float64(f.views), math.Max(1, float64(f.plays)))
	}
	if len(picked) == 0 {
		return []RecapTitle{{Key: "recorder", Name: "记录者", Detail: fmt.Sprintf("本期留下 %d 条观看记录", total)}}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		if picked[i].strength != picked[j].strength {
			return picked[i].strength > picked[j].strength
		}
		return picked[i].order < picked[j].order
	})
	titles := make([]RecapTitle, 0, 3)
	for _, candidate := range picked {
		if len(titles) == 3 {
			break
		}
		titles = append(titles, candidate.title)
	}
	return titles
}
