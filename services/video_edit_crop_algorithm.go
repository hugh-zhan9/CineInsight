package services

import "math"

const (
	editCropSampleWindows = 7
	editCropSampleFrames  = 5
	// Five planar 4:2:0 samples use at most 15,552,000 bytes per seek,
	// below runVideoEditOutput's 16 MiB cap.
	editCropSamplePixels = 1920 * 1080
	// Luma output uses full range. Permit lifted blacks and low-amplitude
	// compression noise without treating generally dark content as a border.
	editCropBarLuma    = 24
	editCropEdgeLuma   = 38
	editCropEdgeChroma = 22
)

// editCropSampleSize preserves display geometry (including rotated/anamorphic
// sources), with full pixel resolution through 1080p and a bounded 4K+ probe.
func editCropSampleSize(width, height int) (int, int) {
	if width*height <= editCropSamplePixels {
		return width, height
	}
	scale := math.Sqrt(float64(editCropSamplePixels) / (float64(width) * float64(height)))
	return max(2, int(float64(width)*scale)&^1), max(2, int(float64(height)*scale)&^1)
}

// editCropWindow unions usable independent frames. One genuine less-cropped
// view must keep that part of the picture; all-dark frames are not evidence.
func editCropWindow(raw string, width, height int) *EditCropRect {
	pixels := width * height
	frameSize := pixels + pixels/2
	if width < 4 || height < 4 || width%2 != 0 || height%2 != 0 || len(raw)%frameSize != 0 {
		return nil
	}
	rects := make([]EditCropRect, 0, editCropSampleFrames)
	for offset := 0; offset < len(raw); offset += frameSize {
		y, u, v := offset+pixels, offset+pixels*5/4, offset+frameSize
		if r := editCropFrame(raw[offset:y], raw[y:u], raw[u:v], width, height); r != nil {
			rects = append(rects, *r)
		}
	}
	return editCropUnion(rects)
}

type editCropLine struct {
	moderate, bright, total int
}

func (l editCropLine) dark() bool {
	// Occasional isolated noise and a few compressed edge pixels aren't a
	// reason to keep the whole bar. Widespread bright detail still vetoes.
	return l.moderate*100 <= l.total*3 && l.bright*100 <= l.total
}

func (l editCropLine) content() bool {
	return l.bright*100 >= l.total*12
}

func editCropFrame(luma, cb, cr string, width, height int) *EditCropRect {
	x0, x1 := width/10, width-width/10
	y0, y1 := height/10, height-height/10
	rows, columns := make([]editCropLine, height), make([]editCropLine, width)
	for y := 0; y < height; y++ {
		rows[y].total = width
	}
	for x := 0; x < width; x++ {
		columns[x].total = height
	}
	bright, visible := 0, (x1-x0)*(y1-y0)
	for y := 0; y < height; y++ {
		inY := y >= y0 && y < y1
		yOffset, uvOffset := y*width, y/2*(width/2)
		for x := 0; x < width; x++ {
			yValue := luma[yOffset+x]
			u, v := cb[uvOffset+x/2], cr[uvOffset+x/2]
			// Saturated blue or purple may have very little luma and still
			// be visibly coloured. Use chroma as a veto against trimming it.
			colour := u < 128-editCropEdgeChroma || u > 128+editCropEdgeChroma ||
				v < 128-editCropEdgeChroma || v > 128+editCropEdgeChroma
			if yValue <= editCropBarLuma && !colour {
				continue
			}
			rows[y].moderate++
			columns[x].moderate++
			if yValue <= editCropEdgeLuma && !colour {
				continue
			}
			rows[y].bright++
			columns[x].bright++
			if x >= x0 && x < x1 && inY {
				bright++
			}
		}
	}
	// Fade-to-black/very dark shots do not narrow (or invalidate) a crop.
	if bright*100 < visible*2 {
		// Still retain a genuinely visible detail right at an outside
		// edge, even if the rest of this shot is dark. Two neighbouring
		// lines rule out an isolated bright pixel.
		if editCropHasEdgeDetail(rows) || editCropHasEdgeDetail(columns) {
			return &EditCropRect{Width: width, Height: height}
		}
		return nil
	}
	left := editCropInset(columns, false)
	right := editCropInset(columns, true)
	top := editCropInset(rows, false)
	bottom := editCropInset(rows, true)
	return &EditCropRect{X: left, Y: top, Width: width - left - right, Height: height - top - bottom}
}

func editCropHasEdgeDetail(lines []editCropLine) bool {
	isDetail := func(line editCropLine) bool { return line.bright*100 >= line.total*4 }
	last := len(lines) - 1
	return isDetail(lines[0]) && isDetail(lines[1]) || isDetail(lines[last]) && isDetail(lines[last-1])
}

// editCropInset requires a contiguous dark run starting at the outside, then
// nearby sustained bright content (a real boundary rather than an interior
// low-contrast gradient). Missing/ambiguous edges retain that entire side.
func editCropInset(lines []editCropLine, reverse bool) int {
	line := func(i int) editCropLine {
		if reverse {
			return lines[len(lines)-1-i]
		}
		return lines[i]
	}
	for i := 0; i < len(lines)/2-1; i++ {
		if line(i).dark() {
			continue
		}
		if i < 2 {
			return 0
		}
		count := 0
		for j := i; j < min(len(lines), i+3); j++ {
			if line(j).content() {
				count++
			}
		}
		if count >= 2 {
			return i
		}
		return 0
	}
	return 0
}

// Round every edge outward so the required even grid never cuts additional
// content. When reduced, retain one source-sample pixel for edge blending.
func editCropMapToDisplay(r EditCropRect, sampleW, sampleH, width, height int) EditCropRect {
	bounds := func(start, end, sampled, display int) (int, int) {
		if sampled < display {
			start, end = max(0, start-1), min(sampled, end+1)
		}
		a := (start * display / sampled) &^ 1
		b := ((end*display+sampled-1)/sampled + 1) &^ 1
		return a, min(b, display)
	}
	x1, x2 := bounds(r.X, r.X+r.Width, sampleW, width)
	y1, y2 := bounds(r.Y, r.Y+r.Height, sampleH, height)
	return EditCropRect{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1}
}
