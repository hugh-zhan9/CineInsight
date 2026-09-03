package services

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// 随机算法在 P-005（播放事件账本）里是明确的「零改动」对象：账本只记流水，
// 不参与打分。这条测试用一份固定夹具把打分与排序的结果逐位钉死——夹具不含
// 随机数，任何对 decayedPlayScore / randomSelectionWeights 的改动都会让它变红。
//
// 基线文本取自 P-005 动工之前的一次运行，同文另存于
// .loopx/workspace/2026-09-02-capability-batch/baseline/random-score-order-before-P-005.txt。
const randomScoreOrderBaseline = `rank=1 id=6 score=0.000000 weight=11.000000
rank=2 id=5 score=0.019531 weight=10.980469
rank=3 id=1 score=0.500000 weight=10.500000
rank=4 id=7 score=1.834008 weight=9.165992
rank=5 id=3 score=2.000000 weight=9.000000
rank=6 id=4 score=3.500000 weight=7.500000
rank=7 id=2 score=10.000000 weight=1.000000`

// randomScoreOrderFixture 固定 7 条候选，覆盖：从未播放、只有随机播放次数、
// 只有正式播放次数、两者兼有、刚播过、播过很久、半衰期整数倍。
func randomScoreOrderFixture(now time.Time) []videoScoreRow {
	at := func(days float64) *time.Time {
		moment := now.Add(-time.Duration(days * 24 * float64(time.Hour)))
		return &moment
	}
	return []videoScoreRow{
		{ID: 1, PlayCount: 0, RandomPlayCount: 1, LastPlayedAt: at(90)},
		{ID: 2, PlayCount: 5, RandomPlayCount: 0, LastPlayedAt: nil},
		{ID: 3, PlayCount: 1, RandomPlayCount: 0, LastPlayedAt: at(0)},
		{ID: 4, PlayCount: 2, RandomPlayCount: 3, LastPlayedAt: at(90)},
		{ID: 5, PlayCount: 3, RandomPlayCount: 4, LastPlayedAt: at(810)},
		{ID: 6, PlayCount: 0, RandomPlayCount: 0, LastPlayedAt: nil},
		{ID: 7, PlayCount: 0, RandomPlayCount: 2, LastPlayedAt: at(11.25)},
	}
}

// randomScoreOrderSnapshot 输出「按权重降序、同权重按 ID 升序」的完整名次表。
func randomScoreOrderSnapshot(now time.Time) string {
	rows := randomScoreOrderFixture(now)
	weights, _ := randomSelectionWeights(rows, 2.0, 90, now)
	order := make([]int, len(rows))
	for index := range rows {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		if weights[order[left]] != weights[order[right]] {
			return weights[order[left]] > weights[order[right]]
		}
		return rows[order[left]].ID < rows[order[right]].ID
	})
	lines := make([]string, 0, len(order))
	for rank, index := range order {
		lines = append(lines, fmt.Sprintf("rank=%d id=%d score=%.6f weight=%.6f",
			rank+1, rows[index].ID, decayedPlayScore(rows[index], 2.0, 90, now), weights[index]))
	}
	return strings.Join(lines, "\n")
}

func TestRandomScoreOrderMatchesPreP005Baseline(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	got := randomScoreOrderSnapshot(now)
	if got != randomScoreOrderBaseline {
		t.Fatalf("随机分数序与基线不一致\n--- got ---\n%s\n--- want ---\n%s", got, randomScoreOrderBaseline)
	}
}
