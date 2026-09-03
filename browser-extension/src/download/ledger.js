// 断点账本：把并发、乱序完成的分片，按序交给落盘方。
//
// 为什么要它：分片是并发取的，谁先回来不确定；但文件必须按顺序写，
// 不然断点续传时"已经写了多少字节"和"下一个该写第几片"对不上，
// 续传出来的文件会缺一段或多一段，而且是静默的——播放器要么花屏要么直接不认。
//
// **游标只在写盘成功之后才前进**，这是这个类的全部要害。早先的版本先把一整批
// 连续分片的游标推完、再把它们交出去逐片写，于是：
//   - 暂停正好落在批次中间时，账本记着"5 片都写了"，磁盘上只有 2 片。续跑时按
//     bytesWritten 截断文件，反而把文件零填充出一段空洞，最后还判定成功；
//   - 写盘抛错（配额满）时，重试会因为 index 已经小于游标而被当成重复回包丢掉，
//     一个字节没写却报成功。
// 两条都是静默产出坏文件。所以现在写入这一步必须由账本自己驱动：写成功一片、
// 前进一格，中途停下或抛错时游标停在实际写到的位置上。
export class SequentialWriteLedger {
  constructor({ totalItems, nextIndex = 0, bytesWritten = 0 } = {}) {
    this.totalItems = Number(totalItems) || 0;
    this.nextIndex = Math.max(0, Number(nextIndex) || 0);
    this.bytesWritten = Math.max(0, Number(bytesWritten) || 0);
    this.pending = new Map(); // index -> Uint8Array
  }

  // 登记一个已取回的分片。只登记，不写盘、不动游标。
  // 返回是否收下：早于写入游标的是重复回包，丢掉（留着会让 bytesWritten 重复累加）。
  record(index, bytes) {
    const position = Number(index);
    if (!Number.isInteger(position) || position < this.nextIndex) return false;
    this.pending.set(position, bytes);
    return true;
  }

  // 把手里连续可写的分片按序写出去。
  //
  // write 抛出时游标不动，那一片仍留在 pending 里，由上层的重试重新写；
  // shouldStop 为真时停在片与片之间，此刻 bytesWritten 与磁盘上的字节数严格相等。
  // 返回实际写出去的片数。
  flush(write, shouldStop) {
    let written = 0;
    while (this.pending.has(this.nextIndex)) {
      if (shouldStop && shouldStop()) break;
      const bytes = this.pending.get(this.nextIndex);
      write(bytes);
      this.pending.delete(this.nextIndex);
      this.nextIndex += 1;
      this.bytesWritten += bytes ? bytes.length : 0;
      written += 1;
    }
    return written;
  }

  // 手里攥着多少还不能写的分片。并发度决定它的上限，用来判断要不要暂缓取新分片。
  get heldCount() {
    return this.pending.size;
  }

  get isComplete() {
    return this.totalItems > 0 && this.nextIndex >= this.totalItems;
  }

  // 可以直接存进 chrome.storage 的最小状态。pending 不存：
  // 那些分片还没落盘，重启后本来就该重下。
  snapshot() {
    return { totalItems: this.totalItems, nextIndex: this.nextIndex, bytesWritten: this.bytesWritten };
  }

  static restore(snapshot) {
    return new SequentialWriteLedger({
      totalItems: snapshot && snapshot.totalItems,
      nextIndex: snapshot && snapshot.nextIndex,
      bytesWritten: snapshot && snapshot.bytesWritten
    });
  }
}

// 并发取任务：按序号发起，最多同时 concurrency 个，失败按退避重试。
//
// 与 SequentialWriteLedger 配合时还有一条约束：手里攥着的乱序分片不能无限涨。
// maxHeld 到顶后暂停发起新的，等写入游标追上来——否则一个卡住的分片会让后面
// 几百片全堆在内存里，正是用 OPFS 落盘要避开的事。
export async function runWithConcurrency({
  total,
  startIndex = 0,
  concurrency = 6,
  retries = 3,
  backoffMs = 800,
  maxHeld = 32,
  shouldStop = () => false,
  heldCount = () => 0,
  run,
  onError = null,
  sleep = defaultSleep
}) {
  let nextToStart = startIndex;
  let failure = null;
  const workers = [];

  const takeNext = () => {
    if (failure || shouldStop()) return -1;
    if (nextToStart >= total) return -1;
    const index = nextToStart;
    nextToStart += 1;
    return index;
  };

  const worker = async () => {
    for (;;) {
      // 攥着的太多就先等一等，让写入游标追上来。
      while (!failure && !shouldStop() && heldCount() >= maxHeld) {
        await sleep(50);
      }
      const index = takeNext();
      if (index < 0) return;

      let lastError = null;
      for (let attempt = 0; attempt <= retries; attempt += 1) {
        if (failure || shouldStop()) return;
        try {
          await run(index);
          lastError = null;
          break;
        } catch (error) {
          lastError = error;
          if (onError) onError(index, attempt, error);
          if (attempt < retries) await sleep(backoffMs * Math.pow(2, attempt));
        }
      }
      if (lastError) {
        failure = lastError;
        return;
      }
    }
  };

  const workerCount = Math.max(1, Math.min(concurrency, Math.max(1, total - startIndex)));
  for (let index = 0; index < workerCount; index += 1) workers.push(worker());
  await Promise.all(workers);
  if (failure) throw failure;
}

function defaultSleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
