package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"video-master/models"
)

func TestEditFullCropPoliciesAndProfiles(t *testing.T) {
	recipe, err := defaultEditRecipe(models.VideoEditKindCrop, []uint{1})
	if err != nil || recipe.Crop.PublishPolicy != "replace_full" {
		t.Fatal(recipe, err)
	}
	if editPublishMode("") != "save_copy" {
		t.Fatal("legacy plan became destructive")
	}
	for _, ext := range strings.Split(defaultVideoExtensions, ",") {
		if _, err := fullCropContainerProfile("source"+ext, "mov,mp4,matroska,avi,asf,flv,webm,mpegts,mpeg,rm"); err != nil {
			t.Fatalf("missing %s: %v", ext, err)
		}
	}
	if _, err := fullCropContainerProfile("a.avi", "matroska,webm"); VideoEditErrorCode(err) != "source_container_mismatch" {
		t.Fatal(err)
	}
	if _, err := fullCropContainerProfile("a.unknown", "matroska"); VideoEditErrorCode(err) != "container_profile_unsupported" {
		t.Fatal(err)
	}
	for _, encoder := range []string{"libvpx-vp9", "mpeg4", "rv10"} {
		if strings.Contains(strings.Join(editVideoEncodeArgs(editPlanVideo{Encoder: encoder}), " "), "-preset") {
			t.Fatal("foreign preset", encoder)
		}
	}
}

// 每种profile在真实ffmpeg上生成容器来源，再用正式参数裁剪；不读取用户素材。
func TestVideoEditFullCropContainerProfilesReal(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	for _, ext := range []string{".mp4", ".mkv", ".mov", ".avi", ".wmv", ".flv", ".f4v", ".webm", ".ts", ".3gp", ".mpg", ".vob", ".rm"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "source"+ext)
			profile, err := fullCropContainerProfile(src, "mov,mp4,matroska,avi,asf,flv,webm,mpegts,mpeg,rm")
			if err != nil {
				t.Fatal(err)
			}
			encoder := profile.VideoEncoder
			if encoder == "" {
				encoder = "libx264"
			}
			args := []string{"-v", "error", "-y", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25", "-f", "lavfi", "-i", "sine=sample_rate=48000", "-t", "1.2", "-c:v", encoder, "-b:v", "800000", "-pix_fmt", "yuv420p", "-c:a", profile.AudioEncoder, "-b:a", "128000", "-ac", "2", "-threads", "2", "-f", profile.Muxer, src}
			runFixtureCommand(t, ffmpeg, args...)
			s := NewVideoEditService(nil, NewMediaWorkSlot(), dir)
			s.goos = "linux"
			s.findFFmpeg = func() (string, error) { return ffmpeg, nil }
			s.findFFprobe = func() (string, error) { return ffprobe, nil }
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			probe, err := s.probeEditSource(ctx, src)
			if err != nil {
				t.Fatal(err)
			}
			size, mtime, err := editFileFingerprint(src)
			if err != nil {
				t.Fatal(err)
			}
			sc := &editSourceCtx{path: src, size: size, modTimeNS: mtime, probe: probe, video: models.Video{ID: 1, Path: src}, ok: true}
			plan := editPlan{Kind: "crop", Mode: "precise", Video: s.planVideoSpec(sc, "precise"), Sources: []editPlanSource{{VideoID: 1, Path: src, Size: size, ModTimeNS: mtime}}, DurationMS: 1000, Segments: []editPlanSegment{{VideoStream: probe.Video.Index, DurMS: 1000}}}
			plan.Video.Crop = &EditCropRect{X: 32, Y: 8, Width: 256, Height: 224}
			plan.Video.Width, plan.Video.Height = 256, 224
			plan.Video.CanvasWidth, plan.Video.CanvasHeight = 320, 240
			for _, a := range probe.Audio {
				plan.Audio = append(plan.Audio, editPlanTrack{Channels: a.Channels})
				plan.Segments[0].Audio = append(plan.Segments[0].Audio, editPlanRef{Stream: a.Index})
			}
			if err = s.configureFullCrop(&plan, sc); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "output.part")
			before := sha256OfFile(t, src)
			runFixtureCommand(t, ffmpeg, fullCropArgs(plan, target)...)
			if err = s.verifyEditOutput(ctx, plan, target); err != nil {
				t.Fatal(err)
			}
			if sha256OfFile(t, src) != before || plan.OutputBase+plan.OutputExt != filepath.Base(src) {
				t.Fatal("original changed or output name lost")
			}
			if err = s.sampleFullCrop(ctx, plan); err != nil {
				t.Fatal(err)
			}
			if ext == ".mov" || ext == ".mkv" {
				wrongExt := ".mp4"
				if ext == ".mkv" {
					wrongExt = ".webm"
				}
				wrongPath := filepath.Join(dir, "disguised"+wrongExt)
				raw, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(wrongPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
				badSource := *sc
				badSource.path = wrongPath
				badPlan := plan
				if err = s.configureFullCrop(&badPlan, &badSource); VideoEditErrorCode(err) != "source_container_mismatch" {
					t.Fatalf("disguised %s accepted: %v", ext, err)
				}
			}

			// 请求无法承载的字幕时必须明确失败，不能改容器或丢字幕。
			if !oneOf(profile.Muxer, "matroska", "mp4", "mov", "3gp", "f4v", "webm") {
				bad := plan
				bad.Subtitles = []editPlanTrack{{Codec: "subrip"}}
				if err = s.configureFullCrop(&bad, sc); VideoEditErrorCode(err) != "container_profile_incompatible" {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(src); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVideoEditFullCropMissingEncoderStopsPreflight(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "source.mkv", 4000)
	project := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	size, mtime, err := editFileFingerprint(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	project = mustUpdateRecipe(t, s, project, func(r *EditRecipe) {
		r.Crop.Items[0].Rect = &EditCropRect{Width: 256, Height: 176}
		r.Crop.Items[0].SourceVersion = editCropSourceVersion(size, mtime)
	})
	s.listEncoders = func(context.Context, string) (map[string]bool, error) { return map[string]bool{}, nil }
	pre, err := s.PreflightProject(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pre.Ready || len(pre.Errors) == 0 || pre.Errors[0].Code != "encoder_unavailable" {
		t.Fatalf("preflight=%+v", pre)
	}
	if media.runs != 0 {
		t.Fatal("encoded despite missing encoder")
	}
}

// 历史配方没有publish_policy，不能因新默认而改变成破坏性导出。
func TestVideoEditFullCropLegacyRecipeStaysCopy(t *testing.T) {
	s, media := newFakeEditService(t)
	v := media.addFakeSource(t, t.TempDir(), "legacy.mkv", 4000)
	project := mustEditProject(t, s, models.VideoEditKindCrop, v.ID)
	size, mtime, err := editFileFingerprint(v.Path)
	if err != nil {
		t.Fatal(err)
	}
	project = mustUpdateRecipe(t, s, project, func(r *EditRecipe) {
		r.Crop.PublishPolicy = ""
		r.Crop.Items[0].Rect = &EditCropRect{Width: 256, Height: 176}
		r.Crop.Items[0].SourceVersion = editCropSourceVersion(size, mtime)
	})
	pre, err := s.PreflightProject(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pre.Ready || len(pre.Outputs) != 1 || pre.Outputs[0].PublishMode != "save_copy" || pre.Outputs[0].PlannedName == v.Name {
		t.Fatalf("legacy must remain separate output: %+v", pre)
	}
	if media.runs != 0 {
		t.Fatal("legacy recipe unexpectedly used new full-crop sample encoder")
	}
}

func TestVideoEditFullCropTrackConflictCanDrop(t *testing.T) {
	for _, tc := range []struct {
		ext, format, subtitle string
		channels              int
	}{{".avi", "avi", "subrip", 2}, {".ts", "mpegts", "subrip", 2}, {".mp4", "mov,mp4,m4a,3gp,3g2,mj2", "hdmv_pgs_subtitle", 2}, {".avi", "avi", "", 6}} {
		t.Run(tc.ext+tc.subtitle, func(t *testing.T) {
			s := NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir())
			s.goos = "linux"
			s.containerInspector = func(string, string) (string, error) {
				p, _ := fullCropContainerProfile("a"+tc.ext, tc.format)
				return editProfileIdentity(p), nil
			}
			sc := &editSourceCtx{path: "/source/a" + tc.ext, size: 100, modTimeNS: 200, video: models.Video{ID: 1, Name: "a" + tc.ext}, ok: true, probe: editProbe{FormatName: tc.format, DurationMS: 1000, Video: &editStream{Index: 0, Codec: "h264", Width: 320, Height: 180, FrameRate: "25/1", PixFmt: "yuv420p"}, Audio: []editStream{{Index: 1, Codec: "aac", Channels: tc.channels}}}}
			if tc.subtitle != "" {
				sc.probe.Subtitles = []editStream{{Index: 2, Codec: tc.subtitle}}
			}
			recipe := EditRecipe{V: 1, Crop: &EditCropRecipe{PublishPolicy: "replace_full", Items: []EditCropItem{{VideoID: 1, Rect: &EditCropRect{Width: 256, Height: 176}, SourceVersion: editCropSourceVersion(100, 200)}}}}
			pre := &EditPreflight{}
			plans := s.buildCropPlans(context.Background(), pre, "precise", recipe, map[uint]*editSourceCtx{1: sc}, nil)
			if len(pre.Conflicts) != 1 || len(pre.Errors) == 0 || len(plans) != 1 {
				t.Fatalf("unresolved conflict: %+v %+v", pre, plans)
			}
			conflict := pre.Conflicts[0]
			if len(conflict.Candidates) != 0 || len(conflict.AllowedFills) != 1 || conflict.AllowedFills[0] != "drop" {
				t.Fatalf("not actionable: %+v", conflict)
			}
			if tc.subtitle != "" && len(plans[0].Subtitles) != 1 || tc.subtitle == "" && len(plans[0].Audio) != 1 {
				t.Fatal("silently dropped source track")
			}
			choice := EditTrackChoice{Output: conflict.OutputIndex, VideoID: 0, ScopeVideoID: conflict.ScopeVideoID, Choice: EditTrackChoiceDrop}
			if conflict.Kind == editTrackSubtitle {
				recipe.Tracks.Subtitle = []EditTrackChoice{choice}
			} else {
				recipe.Tracks.Audio = []EditTrackChoice{choice}
			}
			pre = &EditPreflight{}
			plans = s.buildCropPlans(context.Background(), pre, "precise", recipe, map[uint]*editSourceCtx{1: sc}, nil)
			if len(pre.Errors) != 0 || len(pre.Conflicts) != 0 || plans[0].ContainerProfile == nil {
				t.Fatalf("drop did not resolve: %+v", pre)
			}
			if tc.subtitle != "" && len(plans[0].Subtitles) != 0 || tc.subtitle == "" && len(plans[0].Audio) != 0 {
				t.Fatal("explicit drop ignored")
			}
		})
	}
}

func TestVideoEditFullCropDepthPreservedOrRejected(t *testing.T) {
	s := NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir())
	s.goos = "linux"
	for _, tc := range []struct {
		ext, format, codec, pix, trc string
		allowed                      bool
	}{{".webm", "matroska,webm", "vp9", "yuv420p10le", "", true}, {".webm", "matroska,webm", "vp9", "yuv420p12le", "", true}, {".webm", "matroska,webm", "vp9", "yuv420p10le", "smpte2084", true}, {".avi", "avi", "h264", "yuv420p10le", "", false}, {".flv", "flv", "h264", "yuv420p10le", "smpte2084", false}} {
		s.containerInspector = func(string, string) (string, error) {
			p, _ := fullCropContainerProfile("a"+tc.ext, tc.format)
			return editProfileIdentity(p), nil
		}
		sc := &editSourceCtx{path: "/source/a" + tc.ext, probe: editProbe{FormatName: tc.format, Video: &editStream{Codec: tc.codec, PixFmt: tc.pix, ColorTransfer: tc.trc}}}
		plan := editPlan{Video: editPlanVideo{PixFmt: "yuv420p"}}
		err := s.configureFullCrop(&plan, sc)
		if tc.allowed {
			if err != nil || plan.Video.PixFmt != tc.pix || plan.Video.Main10 {
				t.Fatalf("depth lost or HEVC args on VP9: %+v %v", plan.Video, err)
			}
		} else if VideoEditErrorCode(err) != "container_profile_incompatible" {
			t.Fatalf("should reject loss: %v", err)
		}
	}
}

func TestVideoEditFullCropTenBitWebMReal(t *testing.T) {
	ffmpeg, ffprobe := requireEditFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "tenbit.webm")
	runFixtureCommand(t, ffmpeg, "-v", "error", "-y", "-threads", "2", "-filter_threads", "1", "-f", "lavfi", "-i", "testsrc2=s=320x240:r=25", "-t", "1.2", "-vf", "format=yuv420p10le", "-c:v", "libvpx-vp9", "-deadline", "good", "-cpu-used", "4", "-threads", "2", "-b:v", "800000", "-an", "-f", "webm", src)
	s := NewVideoEditService(nil, NewMediaWorkSlot(), dir)
	s.goos = "linux"
	s.findFFmpeg = func() (string, error) { return ffmpeg, nil }
	s.findFFprobe = func() (string, error) { return ffprobe, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	probe, err := s.probeEditSource(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	size, mtime, err := editFileFingerprint(src)
	if err != nil {
		t.Fatal(err)
	}
	sc := &editSourceCtx{path: src, size: size, modTimeNS: mtime, probe: probe, video: models.Video{ID: 1, Path: src}, ok: true}
	plan := editPlan{Kind: "crop", Mode: "precise", Video: s.planVideoSpec(sc, "precise"), Sources: []editPlanSource{{VideoID: 1, Path: src, Size: size, ModTimeNS: mtime}}, DurationMS: 1000, Segments: []editPlanSegment{{VideoStream: probe.Video.Index, DurMS: 1000}}}
	plan.Video.Crop = &EditCropRect{X: 32, Y: 8, Width: 256, Height: 224}
	plan.Video.Width, plan.Video.Height = 256, 224
	plan.Video.CanvasWidth, plan.Video.CanvasHeight = 320, 240
	if err = s.configureFullCrop(&plan, sc); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "result.part")
	runFixtureCommand(t, ffmpeg, fullCropArgs(plan, target)...)
	if err = s.verifyEditOutput(ctx, plan, target); err != nil {
		t.Fatal(err)
	}
	out, err := s.probeEditSource(ctx, target)
	if err != nil || out.Video.PixFmt != "yuv420p10le" {
		t.Fatalf("10bit lost: %+v %v", out.Video, err)
	}
}

// A不能保留字幕、B可保留同序号字幕；选择A的drop绝不能丢掉B的原轨。
func TestVideoEditFullCropDropIsScopedToSource(t *testing.T) {
	s := NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir())
	s.goos = "linux"
	s.containerInspector = func(path, format string) (string, error) {
		p, err := fullCropContainerProfile(path, format)
		if err != nil {
			return "", err
		}
		return editProfileIdentity(p), nil
	}
	makeSource := func(id uint, path, format string) *editSourceCtx {
		return &editSourceCtx{path: path, size: 100, modTimeNS: 200, video: models.Video{ID: id, Name: filepath.Base(path)}, ok: true, probe: editProbe{FormatName: format, DurationMS: 1000, Video: &editStream{Index: 0, Codec: "h264", Width: 320, Height: 180, FrameRate: "25/1", PixFmt: "yuv420p"}, Subtitles: []editStream{{Index: 2, Codec: "subrip"}}}}
	}
	sources := map[uint]*editSourceCtx{1: makeSource(1, "/source/a.avi", "avi"), 2: makeSource(2, "/source/b.mkv", "matroska,webm")}
	recipe := EditRecipe{V: 1, Crop: &EditCropRecipe{PublishPolicy: "replace_full"}}
	for _, id := range []uint{1, 2} {
		recipe.Crop.Items = append(recipe.Crop.Items, EditCropItem{VideoID: id, Rect: &EditCropRect{Width: 256, Height: 176}, SourceVersion: editCropSourceVersion(100, 200)})
	}
	pre := &EditPreflight{}
	plans := s.buildCropPlans(context.Background(), pre, "precise", recipe, sources, nil)
	if len(pre.Conflicts) != 1 || pre.Conflicts[0].ScopeVideoID != 1 || len(plans[1].Subtitles) != 1 {
		t.Fatalf("initial conflicts/compatible track: %+v %+v", pre, plans)
	}
	recipe.Tracks.Subtitle = []EditTrackChoice{{ScopeVideoID: 1, Output: 0, VideoID: 0, Choice: EditTrackChoiceDrop}}
	if err := validateEditRecipeStructure("crop", recipe); err != nil {
		t.Fatal(err)
	}
	pre = &EditPreflight{}
	plans = s.buildCropPlans(context.Background(), pre, "precise", recipe, sources, nil)
	if len(pre.Errors) != 0 || len(pre.Conflicts) != 0 || len(plans) != 2 || len(plans[0].Subtitles) != 0 || len(plans[1].Subtitles) != 1 || plans[1].Segments[0].Subtitles[0].Stream != 2 {
		t.Fatalf("A drop damaged B: %+v %+v", pre, plans)
	}
	recipe.Tracks.Subtitle = append(recipe.Tracks.Subtitle, EditTrackChoice{ScopeVideoID: 2, Output: 0, VideoID: 0, Choice: EditTrackChoiceDrop})
	if err := validateEditRecipeStructure("crop", recipe); err != nil {
		t.Fatalf("independent source choices treated as duplicates: %v", err)
	}
	// 老crop全局drop此前不生效；升级后不应突然生效于全部原片。
	recipe.Tracks.Subtitle = []EditTrackChoice{{Output: 0, VideoID: 0, Choice: EditTrackChoiceDrop}}
	pre = &EditPreflight{}
	plans = s.buildCropPlans(context.Background(), pre, "precise", recipe, sources, nil)
	if len(pre.Conflicts) != 1 || len(plans[0].Subtitles) != 1 || len(plans[1].Subtitles) != 1 {
		t.Fatal("legacy unscoped crop choice became destructive")
	}
}

func TestVideoEditFullCropVP9HDRSummary(t *testing.T) {
	s := NewVideoEditService(nil, NewMediaWorkSlot(), t.TempDir())
	s.diskFree = func(string) (uint64, error) { return 1 << 40, nil }
	pre := &EditPreflight{}
	plan := editPlan{Seq: 1, PublishMode: "replace_source", Container: "webm", ContainerProfile: &editContainerProfile{Muxer: "webm", VideoCodec: "vp9", AudioCodec: "opus"}, Video: editPlanVideo{Codec: "vp9", Encoder: "libvpx-vp9", PixFmt: "yuv420p10le", Main10: false, ColorTransfer: "smpte2084"}, Sources: []editPlanSource{{Path: "/source/hdr.webm"}}}
	s.finishEditOutputs(context.Background(), pre, []editPlan{plan})
	if len(pre.Errors) != 0 || len(pre.Outputs) != 1 || !pre.Outputs[0].HDR {
		t.Fatalf("VP9 HDR mislabeled: %+v", pre)
	}
}
