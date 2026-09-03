import test from 'node:test';
import assert from 'node:assert/strict';

import { SequentialWriteLedger, runWithConcurrency } from '../../src/download/ledger.js';

const bytes = (length, fill) => new Uint8Array(length).fill(fill);

// 假的落盘方：记下真正被写进去的字节，用来对照账本说的数。
function fakeSink() {
  const written = [];
  const sink = (chunk) => written.push(chunk);
  sink.written = written;
  sink.total = () => written.reduce((sum, chunk) => sum + chunk.length, 0);
  return sink;
}

test('乱序完成的分片按序写出，游标与字节数跟着实际写入走', () => {
  const ledger = new SequentialWriteLedger({ totalItems: 4 });
  const sink = fakeSink();

  ledger.record(2, bytes(20, 2));
  ledger.flush(sink);
  assert.equal(ledger.nextIndex, 0, '第 0 片没到，不该写出任何东西');
  assert.equal(ledger.heldCount, 1);

  ledger.record(0, bytes(10, 0));
  ledger.flush(sink);
  assert.equal(ledger.bytesWritten, 10);
  assert.equal(sink.total(), 10);

  // 1 到位后，2 也跟着被写出去
  ledger.record(1, bytes(15, 1));
  ledger.flush(sink);
  assert.equal(ledger.nextIndex, 3);
  assert.equal(ledger.bytesWritten, 45);
  assert.equal(sink.total(), 45);
  assert.equal(ledger.heldCount, 0);
  assert.equal(ledger.isComplete, false);

  ledger.record(3, bytes(5, 3));
  ledger.flush(sink);
  assert.equal(ledger.isComplete, true);
  assert.equal(ledger.bytesWritten, 50);
  assert.equal(sink.total(), 50);
});

// 这条是回归用例：早先的版本先把一整批的游标推完再逐片写，暂停落在批次中间时
// 账本会记着"全写了"，续跑按这个字节数截断文件，等于在文件里挖一个零填充的洞。
test('暂停落在一批的中间时，账本记的字节数等于磁盘上真有的字节数', () => {
  const ledger = new SequentialWriteLedger({ totalItems: 5 });
  const sink = fakeSink();

  // 1..4 先到，0 最后到——此时 0..4 一次性连续可写
  for (let index = 1; index < 5; index += 1) ledger.record(index, bytes(100, index));
  ledger.record(0, bytes(100, 0));

  // 写到第 2 片时用户按了暂停
  let writtenCount = 0;
  const stopAfterTwo = () => writtenCount >= 2;
  ledger.flush((chunk) => { sink(chunk); writtenCount += 1; }, stopAfterTwo);

  assert.equal(sink.total(), 200, '实际只写了两片');
  assert.equal(ledger.bytesWritten, 200, '账本必须只记两片，否则续传会零填充出一段空洞');
  assert.equal(ledger.nextIndex, 2);
  // 剩下的仍在手里，没有被丢掉
  assert.equal(ledger.heldCount, 3);
});

test('写盘抛错时游标不动，重下的那一片还能被收下并重新写', () => {
  const ledger = new SequentialWriteLedger({ totalItems: 2 });
  const sink = fakeSink();

  ledger.record(0, bytes(64, 0));
  assert.throws(() => ledger.flush(() => { throw new Error('磁盘满了'); }));
  assert.equal(ledger.nextIndex, 0, '写失败不能推进游标');
  assert.equal(ledger.bytesWritten, 0);

  // 上层重试重新取回同一片：必须收下（早先的版本会因为 index < nextIndex 丢弃它，
  // 于是一个字节没写却报成功）
  assert.equal(ledger.record(0, bytes(64, 0)), true);
  ledger.flush(sink);
  assert.equal(ledger.bytesWritten, 64);
  assert.equal(sink.total(), 64);
});

test('重复回包不会把字节数算两遍', () => {
  const ledger = new SequentialWriteLedger({ totalItems: 2 });
  const sink = fakeSink();
  ledger.record(0, bytes(10, 0));
  ledger.flush(sink);
  assert.equal(ledger.record(0, bytes(10, 0)), false);
  ledger.flush(sink);
  assert.equal(ledger.bytesWritten, 10);
  assert.equal(sink.total(), 10);
  assert.equal(ledger.nextIndex, 1);
});

test('账本快照可以原样还原，续跑从下一片开始', () => {
  const ledger = new SequentialWriteLedger({ totalItems: 100 });
  const sink = fakeSink();
  ledger.record(0, bytes(64, 0));
  ledger.record(1, bytes(64, 1));
  ledger.flush(sink);

  const restored = SequentialWriteLedger.restore(ledger.snapshot());
  assert.equal(restored.nextIndex, 2);
  assert.equal(restored.bytesWritten, 128);
  assert.equal(restored.totalItems, 100);
  assert.equal(restored.heldCount, 0);
});

test('并发取数按并发度铺开，全部分片都被取到一次', async () => {
  const seen = [];
  let inFlight = 0;
  let peak = 0;

  await runWithConcurrency({
    total: 20,
    concurrency: 4,
    sleep: async () => {},
    run: async (index) => {
      inFlight += 1;
      peak = Math.max(peak, inFlight);
      await Promise.resolve();
      seen.push(index);
      inFlight -= 1;
    }
  });

  assert.equal(seen.length, 20);
  assert.deepEqual([...seen].sort((a, b) => a - b), Array.from({ length: 20 }, (_, i) => i));
  assert.ok(peak <= 4, `并发峰值 ${peak} 超过了并发度`);
});

test('从断点续跑时不会重取已经写过的分片', async () => {
  const seen = [];
  await runWithConcurrency({
    total: 10,
    startIndex: 6,
    concurrency: 3,
    sleep: async () => {},
    run: async (index) => { seen.push(index); }
  });
  assert.deepEqual([...seen].sort((a, b) => a - b), [6, 7, 8, 9]);
});

test('单个分片失败会重试，重试成功不影响整体', async () => {
  const attempts = new Map();
  await runWithConcurrency({
    total: 3,
    concurrency: 2,
    retries: 3,
    sleep: async () => {},
    run: async (index) => {
      const count = (attempts.get(index) || 0) + 1;
      attempts.set(index, count);
      if (index === 1 && count < 3) throw new Error('临时失败');
    }
  });
  assert.equal(attempts.get(1), 3);
});

test('重试用尽则整体失败，错误原样抛出', async () => {
  await assert.rejects(
    runWithConcurrency({
      total: 3,
      concurrency: 2,
      retries: 2,
      sleep: async () => {},
      run: async (index) => {
        if (index === 2) throw new Error('这一片取不到');
      }
    }),
    /这一片取不到/
  );
});

test('shouldStop 为真时立刻收手，不再发起新的取数', async () => {
  let stopped = false;
  const seen = [];
  await runWithConcurrency({
    total: 100,
    concurrency: 2,
    sleep: async () => {},
    shouldStop: () => stopped,
    run: async (index) => {
      seen.push(index);
      if (seen.length >= 5) stopped = true;
    }
  });
  assert.ok(seen.length < 100, `收手后仍取了 ${seen.length} 片`);
});

test('攥在手里的分片到上限后暂停发起，不把内存堆满', async () => {
  let held = 0;
  let peakHeld = 0;
  const waits = [];

  await runWithConcurrency({
    total: 40,
    concurrency: 8,
    maxHeld: 4,
    heldCount: () => held,
    sleep: async (ms) => { waits.push(ms); held = Math.max(0, held - 2); },
    run: async () => {
      held += 1;
      peakHeld = Math.max(peakHeld, held);
    }
  });

  assert.ok(waits.length > 0, '从未因为攥太多而等待');
  // 上限是"发起前检查"，允许在检查之后再被并发拉高一个批次，但不该失控。
  assert.ok(peakHeld <= 4 + 8, `攥在手里的峰值 ${peakHeld} 失控`);
});
