package services

import "math"

const (
	editCropSampleWindows  = 11
	editCropSampleFrames   = 2
	editCropSamplePixels   = 1920 * 1080
	editCropBarLuma        = 18
	editCropBarMeanMax     = 16.5
	editCropBarVarMax      = 12.0
	editCropBarGradientMax = 5.0
	editCropEdgeLuma       = 36
	editCropEdgeChroma     = 18
	editCropStepMinDelta   = 20.0
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

type editCropEdgeKind uint8

const (
	edgeAmbiguous editCropEdgeKind = iota
	edgeOpen
	edgeBorder
)

const (
	editCropLeft = iota
	editCropRight
	editCropTop
	editCropBottom
)

type editCropSideObs struct {
	kind          editCropEdgeKind
	inset         int
	barMean       float64
	stepBrightPct int
}

type editCropWindowObservation [4]editCropSideObs

func editCropWindowObs(raw string, width, height int) *editCropWindowObservation {
	pixels := width * height
	frameSize := pixels + pixels/2
	if width < 4 || height < 4 || width%2 != 0 || height%2 != 0 || len(raw)%frameSize != 0 {
		return nil
	}
	var window *editCropWindowObservation
	for offset := 0; offset < len(raw); offset += frameSize {
		y, u, v := offset+pixels, offset+pixels*5/4, offset+frameSize
		frame := editCropFrame(raw[offset:y], raw[y:u], raw[u:v], width, height)
		if frame == nil {
			continue
		}
		if window == nil {
			window = frame
			continue
		}
		for side, obs := range frame {
			current := window[side]
			if obs.kind == edgeOpen || obs.kind == edgeBorder &&
				(current.kind == edgeAmbiguous || current.kind == edgeBorder && obs.inset < current.inset) {
				window[side] = obs
			}
		}
	}
	return window
}

type editCropLine struct {
	total, nonBar, bright, sumY          int
	inlierCount, inlierSumY, inlierSumY2 int
}

func (l editCropLine) dark() bool {
	if l.nonBar > max(1, l.total/100) || l.bright > max(1, l.total/200) || l.inlierCount == 0 {
		return false
	}
	n := float64(l.inlierCount)
	mean := float64(l.inlierSumY) / n
	variance := float64(l.inlierSumY2)/n - mean*mean
	return mean <= editCropBarMeanMax && variance <= editCropBarVarMax
}

func (l editCropLine) mean() float64 {
	return float64(l.sumY) / float64(l.total)
}

func (l editCropLine) openDetail() bool {
	return !l.dark() && (l.bright*100 >= l.total*4 || l.mean() >= 24)
}

func (l editCropLine) stepContent(barMean float64) bool {
	return l.bright*100 >= l.total*25 && l.mean()-barMean >= editCropStepMinDelta
}

func editCropFrame(luma, cb, cr string, width, height int) *editCropWindowObservation {
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
		row := &rows[y]
		for x := 0; x < width; x++ {
			column := &columns[x]
			yValue := int(luma[yOffset+x])
			row.sumY += yValue
			column.sumY += yValue
			u, v := cb[uvOffset+x/2], cr[uvOffset+x/2]
			// Saturated blue or purple may have very little luma and still
			// be visibly coloured. Use chroma as a veto against trimming it.
			colour := u < 128-editCropEdgeChroma || u > 128+editCropEdgeChroma ||
				v < 128-editCropEdgeChroma || v > 128+editCropEdgeChroma
			if yValue <= editCropBarLuma && !colour {
				square := yValue * yValue
				row.inlierCount++
				column.inlierCount++
				row.inlierSumY += yValue
				column.inlierSumY += yValue
				row.inlierSumY2 += square
				column.inlierSumY2 += square
				continue
			}
			row.nonBar++
			column.nonBar++
			if yValue <= editCropEdgeLuma && !colour {
				continue
			}
			row.bright++
			column.bright++
			if x >= x0 && x < x1 && inY {
				bright++
			}
		}
	}
	// Fade-to-black/very dark shots do not narrow (or invalidate) a crop.
	if bright*100 < visible*5 {
		// Still retain genuinely visible detail right at an outside edge,
		// vetoing only the side(s) that actually carry edge detail.
		return editCropEdgeDetailObs(rows, columns)
	}
	return &editCropWindowObservation{
		editCropLeft:   editCropMeasureSide(columns, false),
		editCropRight:  editCropMeasureSide(columns, true),
		editCropTop:    editCropMeasureSide(rows, false),
		editCropBottom: editCropMeasureSide(rows, true),
	}
}

func editCropEdgeDetailObs(rows, columns []editCropLine) *editCropWindowObservation {
	isDetail := func(line editCropLine) bool { return line.bright*100 >= line.total*4 }
	var obs editCropWindowObservation
	hasDetail := false
	mark := func(side int, a, b editCropLine) {
		if isDetail(a) && isDetail(b) {
			obs[side] = editCropSideObs{kind: edgeOpen}
			hasDetail = true
		}
	}
	lastCol, lastRow := len(columns)-1, len(rows)-1
	mark(editCropLeft, columns[0], columns[1])
	mark(editCropRight, columns[lastCol], columns[lastCol-1])
	mark(editCropTop, rows[0], rows[1])
	mark(editCropBottom, rows[lastRow], rows[lastRow-1])
	if !hasDetail {
		return nil
	}
	return &obs
}

func editCropMeasureSide(lines []editCropLine, reverse bool) editCropSideObs {
	line := func(i int) editCropLine {
		if reverse {
			return lines[len(lines)-1-i]
		}
		return lines[i]
	}
	open := 0
	for i := 0; i < 3; i++ {
		if line(i).openDetail() {
			open++
		}
	}
	if open >= 2 {
		return editCropSideObs{kind: edgeOpen}
	}
	limit := len(lines) / 2
	i := 0
	for i < limit && line(i).dark() {
		i++
	}
	if i < 2 || i == limit {
		return editCropSideObs{kind: edgeAmbiguous}
	}

	barMean := line(0).mean()
	minMean, maxMean := barMean, barMean
	if i >= 3 {
		minMean = line(1).mean()
		maxMean = minMean
	}
	for k := 1; k < i; k++ {
		m := line(k).mean()
		barMean += m
		minMean, maxMean = min(minMean, m), max(maxMean, m)
	}
	if maxMean-minMean > editCropBarGradientMax {
		return editCropSideObs{kind: edgeAmbiguous}
	}
	barMean /= float64(i)

	stepCount, maxPct := 0, 0
	for j := i; j < min(len(lines), i+4); j++ {
		l := line(j)
		if l.stepContent(barMean) {
			stepCount++
			maxPct = max(maxPct, l.bright*100/l.total)
		}
	}
	if stepCount < 2 {
		return editCropSideObs{kind: edgeAmbiguous}
	}
	return editCropSideObs{kind: edgeBorder, inset: i, barMean: barMean, stepBrightPct: maxPct}
}

func editCropResolve(windows []editCropWindowObservation, sampleW, sampleH, displayW, displayH int) (string, *EditCropRect) {
	n := len(windows)
	if n < 4 {
		return "undetected", nil
	}

	type consensus struct {
		inset, clusterCount       int
		avgBarMean, avgStepBright float64
	}
	resolveSide := func(side, sampleDim int) consensus {
		minInset := 0
		for _, window := range windows {
			obs := window[side]
			switch obs.kind {
			case edgeOpen:
				return consensus{}
			case edgeBorder:
				if minInset == 0 || obs.inset < minInset {
					minInset = obs.inset
				}
			}
		}
		if minInset == 0 {
			return consensus{}
		}
		tol := max(2, min(4, sampleDim/300))
		c := consensus{inset: minInset}
		for _, window := range windows {
			obs := window[side]
			if obs.kind == edgeBorder && obs.inset <= minInset+tol {
				c.clusterCount++
				c.avgBarMean += obs.barMean
				c.avgStepBright += float64(obs.stepBrightPct)
			}
		}
		if c.clusterCount < max(3, (n+1)/2) {
			return consensus{}
		}
		c.avgBarMean /= float64(c.clusterCount)
		c.avgStepBright /= float64(c.clusterCount)
		return c
	}

	lf, rt := resolveSide(editCropLeft, sampleW), resolveSide(editCropRight, sampleW)
	tp, bt := resolveSide(editCropTop, sampleH), resolveSide(editCropBottom, sampleH)
	mapped := editCropMapToDisplay(EditCropRect{
		X: lf.inset, Y: tp.inset,
		Width: sampleW - lf.inset - rt.inset, Height: sampleH - tp.inset - bt.inset,
	}, sampleW, sampleH, displayW, displayH)

	gateAxis := func(a, b, dim int, first, second consensus) (int, int) {
		minPaired := max(8, dim*15/1000)
		if a < minPaired {
			a = 0
		}
		if b < minPaired {
			b = 0
		}
		unpaired := func(inset int, c consensus) bool {
			return inset >= max(16, dim*4/100) && c.clusterCount >= max(4, (n*3+3)/4) &&
				c.avgBarMean <= 10.0 && c.avgStepBright >= 35
		}
		if a > 0 && b == 0 && !unpaired(a, first) {
			a = 0
		}
		if b > 0 && a == 0 && !unpaired(b, second) {
			b = 0
		}
		return a, b
	}
	left, right := gateAxis(mapped.X, displayW-mapped.X-mapped.Width, displayW, lf, rt)
	top, bottom := gateAxis(mapped.Y, displayH-mapped.Y-mapped.Height, displayH, tp, bt)
	rect := EditCropRect{X: left, Y: top, Width: displayW - left - right, Height: displayH - top - bottom}
	full := EditCropRect{Width: displayW, Height: displayH}
	if rect == full || int64(rect.Width)*int64(rect.Height)*100 > int64(displayW)*int64(displayH)*97 {
		return "no_borders", &full
	}
	if rect.Width*100 < displayW*20 || rect.Height*100 < displayH*20 {
		return "undetected", nil
	}
	return "detected", &rect
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
