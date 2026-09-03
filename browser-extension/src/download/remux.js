// MPEG-TS → MP4 转封装（无损，不重新编码）。
//
// 为什么需要它：HLS 的分片绝大多数是 MPEG-TS，直接拼起来只能得到 .ts。用户要的是
// mp4，而改扩展名会得到一个封装格式和后缀对不上的坏文件，比 .ts 更糟。所以真做转封装，
// 用 mux.js（Apache-2.0，已固化在 vendor/）。
//
// 两条设计约束：
//
//  1. **不在下载过程中转**。下载阶段照旧把原始 TS 落到 OPFS，断点续传因此完好；
//     转封装是下载完成后单独一趟。边下边转的话，续传要从头重放整个转封装状态机。
//  2. **流式转，不整份读进内存**。按 188 字节（TS 包长）的整数倍分块读，每块推给
//     转封装器后立刻取走产出写盘。分块对齐包边界是必须的——从半个包中间切开再 flush，
//     那个包会被丢掉。

// TS 包长。分块大小必须是它的整数倍，否则会从包中间切断。
const TS_PACKET_SIZE = 188;
// 每次读多少：约 8 MiB 且对齐包边界。太小会产出过多碎片，太大就失去了流式的意义。
export const REMUX_CHUNK_SIZE = TS_PACKET_SIZE * 45000;

// remuxTsToMp4 把 readChunk 读到的 TS 数据转成 fragmented MP4，逐段交给 write。
//
// 依赖全部注入，因此这套逻辑可以在 Node 里拿真 TS 文件跑，不需要浏览器：
//   muxjs      转封装库（vendor/mux-mp4.min.js 暴露的那个全局）
//   readChunk  (offset, length) => Uint8Array，读到末尾返回长度 0
//   write      (bytes) => void，按调用顺序写出去就是最终的 mp4
//   totalBytes 用于进度上报，可以为 0
export async function remuxTsToMp4({ muxjs, readChunk, write, totalBytes = 0, onProgress = () => {}, shouldStop = () => false }) {
  // mp4 专用构建把 Transmuxer 放在顶层，全量构建放在 .mp4 下面。两种都接。
  const Transmuxer = (muxjs && muxjs.mp4 && muxjs.mp4.Transmuxer) || (muxjs && muxjs.Transmuxer);
  if (!Transmuxer) {
    throw new Error('转封装库没有加载起来，无法输出 mp4');
  }

  const transmuxer = new Transmuxer({ remux: true });
  let initWritten = false;
  let outputBytes = 0;
  let sawData = false;

  transmuxer.on('data', (segment) => {
    sawData = true;
    // 初始化段只写一次：它是整个 mp4 的头，后面每个 fragment 都复用它。
    if (!initWritten && segment.initSegment && segment.initSegment.length > 0) {
      write(segment.initSegment);
      outputBytes += segment.initSegment.length;
      initWritten = true;
    }
    if (segment.data && segment.data.length > 0) {
      write(segment.data);
      outputBytes += segment.data.length;
    }
  });

  let offset = 0;
  for (;;) {
    if (shouldStop()) throw new Error('转封装被取消');
    const chunk = await readChunk(offset, REMUX_CHUNK_SIZE);
    if (!chunk || chunk.length === 0) break;
    offset += chunk.length;

    transmuxer.push(chunk);
    // 每块推完立刻 flush 取走产出：不 flush 的话产物全堆在转封装器内部，
    // 一部几个 GB 的片子会把内存吃光——那正是用 OPFS 要避开的事。
    // 分块按 188 对齐，所以在这里 flush 不会切断任何一个 TS 包。
    transmuxer.flush();
    onProgress({ readBytes: offset, totalBytes, outputBytes });
  }

  transmuxer.flush();

  if (!sawData || outputBytes === 0) {
    // 一个字节都没产出，说明这份数据不是转封装器认得的 TS。与其交出一个空文件，
    // 不如明确失败——用户至少知道该去复制 ffmpeg 命令自己处理。
    throw new Error('转封装没有产出任何数据：这条流可能不是标准的 MPEG-TS');
  }
  onProgress({ readBytes: offset, totalBytes, outputBytes });
  return { readBytes: offset, outputBytes };
}
