package editalign

import "testing"

func TestHashGray9x8BitOrder(t *testing.T) {
	descending := make([]byte, 72)
	for row := 0; row < 8; row++ {
		for column := 0; column < 9; column++ {
			descending[row*9+column] = byte(200 - column*10)
		}
	}
	if got, _ := HashGray9x8(descending); got != ^uint64(0) {
		t.Fatalf("descending rows = %016x, want all ones", got)
	}

	flat := make([]byte, 72)
	if got, _ := HashGray9x8(flat); got != 0 {
		t.Fatalf("flat = %016x, want 0", got)
	}

	// 行优先、高位在前：第 0 行第 0 对是最高位，第 7 行第 7 对是最低位。
	first := make([]byte, 72)
	first[0] = 1
	if got, _ := HashGray9x8(first); got != 1<<63 {
		t.Fatalf("row0 pair0 = %016x, want %016x", got, uint64(1)<<63)
	}
	last := make([]byte, 72)
	last[7*9+7] = 1
	if got, _ := HashGray9x8(last); got != 1 {
		t.Fatalf("row7 pair7 = %016x, want 1", got)
	}
	second := make([]byte, 72)
	second[1*9+0] = 1 // 第 1 行第 0 对 → 第 8 位（自高位起）
	if got, _ := HashGray9x8(second); got != 1<<55 {
		t.Fatalf("row1 pair0 = %016x, want %016x", got, uint64(1)<<55)
	}
	// 相等不记 1（严格大于）。
	equal := make([]byte, 72)
	for column := 0; column < 9; column++ {
		equal[column] = 5
	}
	if got, _ := HashGray9x8(equal); got != 0 {
		t.Fatalf("equal neighbours = %016x, want 0", got)
	}
}

func TestHashGray9x8RejectsWrongLength(t *testing.T) {
	for _, size := range []int{0, 71, 73, 64} {
		if _, err := HashGray9x8(make([]byte, size)); err == nil {
			t.Fatalf("size %d: expected error", size)
		}
	}
}

func TestInformativeBounds(t *testing.T) {
	cases := map[int]bool{0: false, 7: false, 8: true, 32: true, 56: true, 57: false, 64: false}
	for ones, want := range cases {
		hash := uint64(0)
		if ones == 64 {
			hash = ^uint64(0)
		} else {
			hash = (uint64(1) << ones) - 1
		}
		if got := Informative(hash); got != want {
			t.Errorf("popcount %d: Informative = %v, want %v", ones, got, want)
		}
	}
}

func TestScrubPathsRemovesAbsolutePaths(t *testing.T) {
	text := "/Users/me/My Movies/a b.mkv: Invalid data; tmp /var/folders/x/editalign-pts-1.txt, C:\\Videos\\x.mp4."
	got := scrubPaths(text, "/Users/me/My Movies/a b.mkv")
	want := "<path>: Invalid data; tmp <path>, <path>."
	if got != want {
		t.Fatalf("scrubPaths = %q, want %q", got, want)
	}
}
