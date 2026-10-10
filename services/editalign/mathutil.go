package editalign

import "math/bits"

// hamming 是两枚 dHash 的汉明距离。
func hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// roundDiv 是四舍五入的整数除法（den>0），负数向远离零的方向取半。
func roundDiv(num, den int64) int64 {
	if num >= 0 {
		return (num + den/2) / den
	}
	return -((-num + den/2) / den)
}

// floorDiv 是向下取整的整数除法（den>0）。
func floorDiv(num, den int64) int64 {
	q := num / den
	if num%den != 0 && num < 0 {
		q--
	}
	return q
}
