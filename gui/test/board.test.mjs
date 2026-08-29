// 棋盘视图单元测试：node test/board.test.mjs
// BoardView 深度依赖 canvas，这里用 Proxy 伪 ctx + 伪 canvas 桩掉绘制，
// 只测与交互/几何相关的纯逻辑：px 坐标映射、cellAt 命中/边缘/出盘判定、
// setState 的默认值。浏览器专属的 requestAnimationFrame/render 路径不测。
import assert from 'node:assert/strict';
import { BoardView } from '../public/js/board.js';

// 伪 2D 上下文：任何属性可写、任何方法可调；渐变工厂返回带 addColorStop 的对象。
function fakeCtx() {
  return new Proxy({}, {
    get(_t, key) {
      if (key === 'createLinearGradient' || key === 'createRadialGradient') {
        return () => ({ addColorStop() {} });
      }
      return () => {};
    },
    set() { return true; },
  });
}

function fakeCanvas() {
  const listeners = {};
  const canvas = {
    style: {},
    width: 0,
    height: 0,
    parentElement: { clientWidth: 600, clientHeight: 600 },
    getContext: () => fakeCtx(),
    addEventListener: (name, fn) => { (listeners[name] ??= []).push(fn); },
    getBoundingClientRect: () => ({ left: 0, top: 0, width: canvas.width, height: canvas.height }),
    _listeners: listeners,
  };
  return canvas;
}

// node 没有 window/devicePixelRatio：resize() 里以 typeof 守卫的 ResizeObserver
// 不存在会跳过，但 window.devicePixelRatio 直接引用，这里补一个最小桩。
globalThis.window = { devicePixelRatio: 1 };

const tests = [];
const test = (name, fn) => tests.push([name, fn]);

function mkView(size) {
  const canvas = fakeCanvas();
  const clicks = [];
  const view = new BoardView(canvas, (x, y) => clicks.push({ x, y }));
  view.setState({
    size,
    getStone: () => 0,
    interactive: true,
  });
  return { view, canvas, clicks };
}

test('px(i) 是线性网格映射，格宽覆盖整个画布', () => {
  const { view } = mkView(11);
  const { margin, cell } = view;
  assert.equal(view.px(0), margin);
  assert.ok(Math.abs(view.px(5) - (margin + 5 * cell)) < 1e-9);
  assert.ok(Math.abs(view.px(10) - (600 - margin)) < 1e-6); // 末线贴住右边距
});

test('cellAt：交叉点命中返回 {x,y}', () => {
  const { view } = mkView(11);
  const cx = view.px(3), cy = view.px(2);
  const c = view.cellAt({ clientX: cx, clientY: cy });
  assert.deepEqual(c, { x: 3, y: 2 });
});

test('cellAt：距交叉点超过 0.47 格宽不命中', () => {
  const { view } = mkView(11);
  const cx = view.px(3) + view.cell * 0.48;
  assert.equal(view.cellAt({ clientX: cx, clientY: view.px(2) }), null);
});

test('cellAt：出盘坐标返回 null', () => {
  const { view } = mkView(11);
  assert.equal(view.cellAt({ clientX: view.px(0) - view.cell * 2, clientY: view.px(2) }), null);
  assert.equal(view.cellAt({ clientX: view.px(3), clientY: view.px(15) }), null);
});

test('cellAt 命中后点击回调收到格子坐标', () => {
  const { view, canvas, clicks } = mkView(11);
  const cx = view.px(5), cy = view.px(6);
  for (const fn of canvas._listeners.click) {
    fn({ clientX: cx, clientY: cy });
  }
  assert.deepEqual(clicks, [{ x: 5, y: 6 }]);
});

test('setState 填充默认值', () => {
  const { view } = mkView(9);
  assert.equal(view.size, 9);
  assert.equal(view.interactive, true);
  assert.deepEqual(view.forbidden, []);
  assert.equal(view.lastMove, null);
  assert.equal(view.winLine, null);
  assert.equal(typeof view.isForbiddenCell({ x: 0, y: 0 }), 'boolean');
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
