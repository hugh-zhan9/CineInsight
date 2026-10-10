package editalign

// 本包全部阈值集中在这里；editalign 的测试把它们钉住，改动任何一个都会改变
// 片头识别与高清对齐的判定（视频编辑合同「分析：片头识别与分段对齐」）。
const (
	// hashGrayBytes 是一帧 9×8 灰度小图的字节数，dHash 的唯一输入形态。
	hashGrayBytes = 9 * 8

	// 合同默认值：零值选项字段取这些值。
	defaultIntroMinDurationMS = 10000
	defaultMaxHamming         = 8
	defaultAnchorStepMS       = 1000
	defaultMinSegmentMS       = 3000
	defaultMergeGapMS         = 3000
	defaultSecondBestMargin   = 3

	// runMaxMismatchMS：一段恒定偏移的连续匹配里，两次命中之间最多容忍的失配时长。
	// 4fps 下是 2 个采样：吸收转码噪声与快剪切点处的采样相位差，再长就断开。
	runMaxMismatchMS = 500
	// runMaxNeutralMS：两侧都是低信息帧（黑场、淡入淡出）时既不计票也不打断，
	// 但连续超过 3 秒就断开，避免一大段黑场把两处无关的命中粘成一段。
	runMaxNeutralMS = 3000
	// introAmbiguityPercent：与最优候选不相交的另一候选，证据达到最优的 90% 即视为
	// "等长"，结果标 ambiguous 而不是猜一个。
	introAmbiguityPercent = 90

	// refineWindowMS：帧级细化在粗边界两侧各取 1 秒原帧率小图。
	refineWindowMS = 1000
	// refineCacheGridMS：细化读帧窗口向外取整到 500ms 网格，参照片被多次读取时可复用。
	refineCacheGridMS = 500
	// refineCacheWindows：每个来源最多缓存的帧窗口数（32×32 时每窗约百 KB）。
	refineCacheWindows = 8
	// madThresholdMin/Max：帧级"相同画面"阈值的夹取范围（0–255 灰度的平均绝对差）。
	// 实际阈值 = 2×内部帧 MAD 的 90 分位 + 3，按这对来源的转码噪声自适应。
	madThresholdMin = 8
	madThresholdMax = 20
	// frameUniformDeviation：小灰度图像素相对均值的平均绝对偏差低于 2 视为均匀画面（黑场等），
	// 帧级细化里两侧都均匀的帧不算匹配。
	frameUniformDeviation = 2.0

	// maxGrayWindowMS / maxGrayFrames：原帧率读取器一次最多读 10 秒、1500 帧，
	// 内存上限约 side²×1500 字节（32×32 时 1.5MB）。
	maxGrayWindowMS = 10000
	maxGrayFrames   = 1500
	maxGraySide     = 64
	// maxHashFrames：单条指纹序列的帧数上限（4fps 下约 277 小时），防止失控输出吃满内存。
	maxHashFrames = 4_000_000
	// stderrTailBytes：错误信息只保留 ffmpeg stderr 尾部 2KB，并擦除路径。
	stderrTailBytes = 2048
	// progressEvery：对齐阶段每处理这么多锚点回调一次进度。
	progressEvery = 64
)
