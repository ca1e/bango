// 协议管理器单元测试：node test/protocol.test.mjs
// EngineManager 与 EngineLink 的唯一浏览器耦合在 fetch/EventSource 上，
// 这里注入一个 FakeLink（onLine/onStatus/send 三件套）覆盖纯编排逻辑：
// 应答配对、MESSAGE/DEBUG 剥离、超时、断连、TAKEBACK/BOARD 应答分型。
import assert from 'node:assert/strict';
import { EngineManager, EngineError, parseCoords } from '../public/js/protocol.js';

// FakeLink：与 EngineLink 相同的回调接口，send 记录发出的行，feed 手动喂应答。
class FakeLink {
  constructor() {
    this.lineCbs = new Set();
    this.statusCbs = new Set();
    this.sent = [];
    this.sendError = null;
  }
  onLine(cb) { this.lineCbs.add(cb); return () => this.lineCbs.delete(cb); }
  onStatus(cb) { this.statusCbs.add(cb); return () => this.statusCbs.delete(cb); }
  async send(lines) {
    if (this.sendError) throw this.sendError;
    const arr = Array.isArray(lines) ? lines : [lines];
    this.sent.push(...arr);
  }
  feed(line) { for (const cb of this.lineCbs) cb(line); }
  drop() { for (const cb of this.statusCbs) cb({ state: 'disconnected', detail: '' }); }
}

const SHORT = 120; // ms：测试里的超时用短值，只验证行为不验证时长

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

test('parseCoords 解析坐标行（容忍空白与负号）', () => {
  assert.deepEqual(parseCoords('7,8'), { x: 7, y: 8 });
  assert.deepEqual(parseCoords(' 10, 9 '), { x: 10, y: 9 });
  assert.deepEqual(parseCoords('-1,3'), { x: -1, y: 3 });
  assert.equal(parseCoords('OK'), null);
  assert.equal(parseCoords('7;8'), null);
  assert.equal(parseCoords('7,8,1'), null);
});

test('parseAbout 提取键值对（小写键）', () => {
  const out = EngineManager.parseAbout(
    `name="pbrain-bango", version="0.1", author="ca1e", country="CN"`);
  assert.equal(out.name, 'pbrain-bango');
  assert.equal(out.version, '0.1');
  assert.equal(out.author, 'ca1e');
  assert.equal(out.country, 'CN');
});

test('expect：匹配应答解析坐标，MESSAGE/DEBUG 剥离进 messages', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const w = m.expect((l) => parseCoords(l), SHORT, 'AI 应答');
  const p = w.promise;
  link.feed('MESSAGE board 15x15');
  link.feed('DEBUG nodes=1234');
  link.feed('7,8');
  const { reply, messages } = await p;
  assert.equal(reply, '7,8');
  assert.deepEqual(messages, ['board 15x15', 'nodes=1234']);
});

test('expect：意外行被忽略，继续等待匹配应答', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const p = m.expect((l) => /^\s*OK\s*$/i.test(l), SHORT, 'START 应答').promise;
  link.feed('7,7');        // 不匹配的着法行
  link.feed('UNKNOWN command: FOO');
  link.feed('OK');
  const { reply } = await p;
  assert.equal(reply, 'OK');
});

test('expect：超时拒绝', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  await assert.rejects(
    m.expect(() => false, 30, '永不到来的应答').promise,
    /超时/);
});

test('expect：连接断开立即拒绝', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const p = m.expect(() => false, 5000, 'START 应答').promise;
  link.drop();
  await assert.rejects(p, /连接已断开/);
});

test('cmd：先挂监听再发送，应答不丢失', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  // FakeLink.send 是同步 resolve，但 cmd 的实现先注册 expect 再 await send，
  // 这里喂应答也放在发送后仍然能配对（顺序敏感性以注入时序验证）。
  const p = m.cmd(['START 15'], (l) => l === 'OK', SHORT, 'START 应答');
  link.feed('OK');
  await p;
  assert.deepEqual(link.sent, ['START 15']);
});

test('cmd：send 失败时取消等待并抛出', async () => {
  const link = new FakeLink();
  link.sendError = new Error('桥接未连接');
  const m = new EngineManager(link);
  await assert.rejects(
    m.cmd(['START 15'], () => true, SHORT, 'START 应答'),
    /桥接未连接/);
});

test('start：OK 正常返回，ERROR 抛 EngineError', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const p = m.start(15);
  link.feed('OK');
  await p;

  const link2 = new FakeLink();
  const m2 = new EngineManager(link2);
  const p2 = m2.start(3);
  link2.feed('ERROR unsupported board size 3');
  await assert.rejects(p2, (e) => e instanceof EngineError && /board size/.test(e.message));
});

test('turn：着法应答解析坐标，ERROR 抛 EngineError', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const p = m.turn(7, 8);
  // 先等 send 完成（微任务）后再喂应答
  await new Promise((r) => setTimeout(r, 0));
  link.feed('MESSAGE thinking...');
  link.feed('6,6');
  const c = await p;
  assert.deepEqual(c, { x: 6, y: 6 });
  assert.deepEqual(link.sent, ['INFO time_left 2147483647', 'TURN 7,8']);

  const link2 = new FakeLink();
  const m2 = new EngineManager(link2);
  const p2 = m2.turn(0, 0);
  await new Promise((r) => setTimeout(r, 0));
  link2.feed('ERROR bad move');
  await assert.rejects(p2, EngineError);
});

test('takeback：OK/UNKNOWN/ERROR 三种应答分型', async () => {
  for (const [line, want] of [
    ['OK', 'ok'],
    ['UNKNOWN command: TAKEBACK', 'unknown'],
    ['ERROR bad takeback', 'error:bad takeback'],
  ]) {
    const link = new FakeLink();
    const m = new EngineManager(link);
    const p = m.takeback(7, 7);
    await new Promise((r) => setTimeout(r, 0));
    link.feed(line);
    assert.equal(await p, want);
  }
});

test('board：构造 BOARD 重放行，着法应答返回 {coords}，OK 应答返回 {ok:true}', async () => {
  const link = new FakeLink();
  const m = new EngineManager(link);
  const p = m.board([
    { x: 7, y: 7, field: 1 },
    { x: 8, y: 8, field: 2 },
    { x: 6, y: 6, field: 3 }, // 标记格原样转发
  ]);
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(link.sent, [
    'INFO time_left 2147483647',
    'BOARD',
    '7,7,1',
    '8,8,2',
    '6,6,3',
    'DONE',
  ]);
  link.feed('9,9');
  assert.deepEqual(await p, { coords: { x: 9, y: 9 } });

  const link2 = new FakeLink();
  const m2 = new EngineManager(link2);
  const p2 = m2.board([{ x: 7, y: 7, field: 1 }]);
  await new Promise((r) => setTimeout(r, 0));
  link2.feed('OK');
  assert.deepEqual(await p2, { ok: true });
});

let failed = 0;
for (const [name, fn] of tests) {
  try {
    await fn();
    console.log(`  ✓ ${name}`);
  } catch (err) {
    failed++;
    console.error(`  ✗ ${name}\n    ${err.message}`);
  }
}
console.log('');
if (failed) {
  console.error(`${failed}/${tests.length} 个用例失败`);
  process.exit(1);
}
console.log(`全部 ${tests.length} 个用例通过`);
