// 禁手与胜负规则单元测试：node test/forbidden.test.mjs
import assert from 'node:assert/strict';
import { Game, BLACK, WHITE } from '../public/js/rules.js';

function mk(size, renju, stones) {
  const g = new Game(size, renju);
  for (const [x, y, c] of stones) g.board[y * size + x] = c;
  return g;
}

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

test('恰五：不禁手且判黑胜', () => {
  const g = mk(15, true, [[6, 9, BLACK], [7, 9, BLACK], [8, 9, BLACK], [9, 9, BLACK]]);
  assert.equal(g.forbiddenAt(10, 9), null);
  const res = g.place(10, 9, true);
  assert.equal(res.over, true);
  assert.equal(res.winner, BLACK);
});

test('长连（六连）是禁手点', () => {
  const g = mk(15, true, [[7, 9, BLACK], [8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK], [12, 9, BLACK]]);
  assert.equal(g.forbiddenAt(11, 9), 'overline');
});

test('黑长连落子判白胜', () => {
  const g = mk(15, true, [[7, 9, BLACK], [8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK], [11, 9, BLACK]]);
  const res = g.place(12, 9, true);
  assert.equal(res.winner, WHITE);
  assert.match(res.reason, /长连/);
});

test('双活三禁手（十字三三）', () => {
  const g = mk(15, true, [[8, 9, BLACK], [10, 9, BLACK], [9, 8, BLACK], [9, 10, BLACK]]);
  assert.equal(g.forbiddenAt(9, 9), 'double-three');
  assert.ok(g.allForbiddenPoints().some((p) => p.x === 9 && p.y === 9));
});

test('活四不算四四（两端同属一个四）', () => {
  const g = mk(15, true, [[8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK]]);
  assert.equal(g.forbiddenAt(7, 9), null);
  assert.equal(g.forbiddenAt(11, 9), null);
});

test('四三不禁手', () => {
  const g = mk(15, true, [[8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK], [11, 8, BLACK], [11, 10, BLACK]]);
  assert.equal(g.forbiddenAt(11, 9), null);
});

test('双眠三不禁手（眠三不计入三三）', () => {
  const g = mk(15, true, [
    [6, 9, WHITE], [7, 9, BLACK], [8, 9, BLACK],
    [9, 6, WHITE], [9, 7, BLACK], [9, 8, BLACK],
  ]);
  assert.equal(g.forbiddenAt(9, 9), null);
});

test('四四禁手（十字双四）', () => {
  const g = mk(15, true, [[8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK], [11, 7, BLACK], [11, 8, BLACK], [11, 10, BLACK]]);
  assert.equal(g.forbiddenAt(11, 9), 'double-four');
});

test('递归：成活点均为四四禁手的假活三不计入三三', () => {
  const g = mk(15, true, [
    [8, 9, BLACK], [10, 9, BLACK],             // 横向活三伙伴
    [9, 7, BLACK], [9, 8, BLACK],              // 纵向“三”伙伴
    [7, 6, BLACK], [8, 6, BLACK], [10, 6, BLACK],   // (9,6) 的横向四
    [7, 10, BLACK], [8, 10, BLACK], [10, 10, BLACK], // (9,10) 的横向四
    [9, 9, BLACK],                              // 候选子已在盘中
  ]);
  assert.equal(g.forbiddenAt(9, 6), 'double-four');
  assert.equal(g.forbiddenAt(9, 10), 'double-four');
  assert.equal(g.forbiddenAt(9, 9), null); // 纵向是假活三 → 只剩一个真活三 → 合法
});

test('无禁手规则：六连也获胜', () => {
  const g = mk(15, false, [[7, 9, BLACK], [8, 9, BLACK], [9, 9, BLACK], [10, 9, BLACK]]);
  const res = g.place(11, 9, true);
  assert.equal(res.winner, BLACK);
});

test('白方六连同样获胜（禁手只约束黑）', () => {
  const g = mk(15, true, [[7, 9, WHITE], [8, 9, WHITE], [9, 9, WHITE], [10, 9, WHITE]]);
  g.moves.push({ x: 0, y: 0, player: BLACK, sent: true });
  g.board[0] = BLACK;
  const res = g.place(11, 9, true);
  assert.equal(res.winner, WHITE);
});

test('无禁手规则下没有禁手点', () => {
  const g = mk(15, false, [[8, 9, BLACK], [10, 9, BLACK], [9, 8, BLACK], [9, 10, BLACK]]);
  assert.deepEqual(g.allForbiddenPoints(), []);
});

let failed = 0;
test('同手恰五+长连并存：五优先判黑胜（非禁手）', () => {
  // 行向落 (7,9) 成恰五，同时列向成七连：RIF 五覆盖一切禁手形状，
  // 与引擎 rules.go 的 winsMove（逐方向 c==5 即胜）口径一致。
  const g = mk(15, true, [
    [3, 9, BLACK], [4, 9, BLACK], [5, 9, BLACK], [6, 9, BLACK],
    [7, 3, BLACK], [7, 4, BLACK], [7, 5, BLACK], [7, 6, BLACK],
    [7, 7, BLACK], [7, 8, BLACK],
  ]);
  assert.equal(g.forbiddenAt(7, 9), null);
  const res = g.place(7, 9, true);
  assert.equal(res.over, true);
  assert.equal(res.winner, BLACK);
});

for (const [name, fn] of tests) {
  try {
    fn();
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
