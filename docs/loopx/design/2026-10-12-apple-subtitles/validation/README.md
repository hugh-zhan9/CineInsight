# 复现实验

这是独立验证脚本，不是应用运行时；不修改媒体库、数据库、现有索引或系统偏好。Core Spotlight 探测只创建专用测试应用的命名索引，并在结束时删除。不要把私有接口程序当成已经受支持的产品实现。

## 环境

本次使用 macOS 27.0 / M5，Command Line Tools macOS 26.5 SDK；评分脚本使用项目已有的 Python 3.10.20 测试环境、NumPy 2.2.6、Pillow 12.3.0。MAT 文件由脚本直接解析，不依赖 SciPy。字幕辅助程序需先按项目脚本构建，系统中英翻译语言资源需已安装。数据和中间结果固定放在 `/tmp/cineinsight-apple-validation`。

仓库不保存测试图片和生物特征向量。LF​​W、Olivetti、COCO 仍以原始发布者的许可与使用条件为准；数据仅用于此次验证。

## 构建与数据

从仓库根目录执行，`PYTHON` 指向已安装 NumPy/Pillow 的 Python 3.10–3.13：

```bash
BASE=/tmp/cineinsight-apple-validation
SRC="$PWD/docs/loopx/design/2026-10-12-apple-subtitles/validation/scripts"
PYTHON="$HOME/.CineInsight/face-runtime/venv/bin/python3"
mkdir -p "$BASE"
cp "$SRC"/* "$BASE"/
xcrun clang -O2 -Wno-deprecated-declarations -Wno-incompatible-pointer-types -framework Foundation -framework Vision "$SRC/vision-worker.m" -o "$BASE/vision-worker"
xcrun swiftc -O -target arm64-apple-macos26.0 "$SRC/embedding-worker.swift" -o "$BASE/embedding-worker"
python3 "$BASE/download.py"
```

`download.py` 会校验 LFW 和 Olivetti 固定 SHA-256。COCO 描述数据从 `https://raw.githubusercontent.com/tylin/coco-caption/master/annotations/captions_val2014.json` 下载为 `$BASE/coco-captions.json`；校验值在 `metrics/dataset-checksums.json`。不要用其他版本的描述文件冒充本次抽样。图片下载使用官方公开 S3 bucket 的有效 TLS 主机，未关闭证书检查。

## 人物实验顺序

```bash
"$PYTHON" "$BASE/prepare_faces.py"
python3 "$BASE/prepare_lfw.py"
python3 "$BASE/prepare_unknown.py"
"$PYTHON" "$BASE/prepare_padding.py"
for name in orl orlpad lfw lfwpad unknown unknownpad; do
  python3 "$BASE/run_worker.py" "$name"
done
"$PYTHON" "$BASE/evaluate_faces.py" orl orlpad lfw lfwpad
"$PYTHON" "$BASE/calibrate_open.py"
# 阈值到此冻结。下面的全新 40 人不参与校准。
"$PYTHON" "$BASE/prepare_fresh_faces.py"
python3 "$BASE/run_worker.py" fresh
"$PYTHON" "$BASE/evaluate_fresh.py"
"$PYTHON" "$BASE/evaluate_large_gallery.py"
```

`run_worker.py` 每条请求最多等待 45 秒，失败不会填充伪造向量。评价脚本会把没有向量的样本计为缺失，分开列“所有查询”和“已提取查询”的口径；聚类使用五个固定输入顺序。多脸图统一选最大脸，这是本次实验约定。

`evaluate_faces.py` 是第一轮配对阈值诊断，不能拿它的阈值替换最终未知者拒绝阈值；最终结论以 `calibrate_open.py`、`evaluate_fresh.py`、`evaluate_large_gallery.py` 为准。汇总指标里的身份只有匿名数字，不报告人名。

## 画面与文字检索

```bash
python3 "$BASE/prepare_scenes.py"
python3 "$BASE/run_worker.py" coco
bash scripts/build_apple_ai.sh
"$PYTHON" "$SRC/evaluate_scene_text.py"
"$PYTHON" "$SRC/evaluate_zh_roundtrip.py"
```

最后两个脚本从其仓库位置向上寻找 `go.mod`，定位已构建的字幕辅助程序；因此从 `$SRC` 执行，不从复制到 `/tmp` 的位置执行。它们分别保存中文查询、分类索引对照与最终统计。

图片变体：对每张 COCO 图生成两种查询，第一种长边缩至 320、JPEG quality=40；第二种先裁去四周各 10%，再使用相同缩放和压缩。原始图片始终留作参考库，查询图不写回原图。生成的请求列表写为 `coco-query-manifest.json`，字段为 `id=<原ID>-compressed/crop`、`path`、`op=image`。可运行 `prepare_image_queries.py` 生成，然后运行 `run_worker.py coco-query` 和 `evaluate_extra.py`。

```bash
"$PYTHON" "$BASE/prepare_video.py"
python3 "$BASE/run_worker.py" face-video
python3 "$BASE/run_worker.py" scene-video
"$PYTHON" "$BASE/evaluate_video.py"
```

视频为有标注静态样本编码成 H.264 CRF=32、640×480 后再抽帧，用于编解码稳健性；不把派生帧算作新的人物或场景样本。

## 其他接口排查

`spotlight-controls.m` 和 `spotlight-en.m` 是 Core Spotlight 对照。构建成独立 `.app`，Bundle ID 使用 `com.cineinsight.validation.spotlight20261012`，专用索引名为 `cineinsight-visual-probe-20261012`。运行前确认这个名称不属于其他实验；不要改成主应用 Bundle ID。它们提交测试图和一条文本，检查结构化、普通词和语义查询，再清理。若进程被中断，用同一程序的 `cleanup` 参数清理其专用索引。

Core Spotlight 普通词控制成功、语义控制无结果，与“图片内容不匹配”不是同一个结论。测试没有观察到模型/语义索引的就绪承诺，所以日志用于说明当前链路未达到替换条件，不能按图片语义准确率 0% 报告。

## 证据边界

报告中的数字以相邻 `metrics` 文件为准。新的 macOS / Vision 模型版本、预处理、阈值、样本或参考库规模都会改变结果。未比较本机当前未安装的 InsightFace / Chinese-CLIP 模型，不声称优于它们。本次对实际使用的 Native 编解码、推理和排序都执行过；没有做完整 Wails UI 的人物引擎切换，也没有执行数据迁移。
