// Canvas 棋盘：暖木质感底、网格/星位/坐标、棋子与状态标记、悬停预览、落子动画。
import { EMPTY, BLACK, WHITE } from './rules.js';

export class BoardView {
  constructor(canvas, onClick) {
    this.canvas = canvas;
    this.onClick = onClick;
    this.ctx = canvas.getContext('2d');
    this.size = 20;
    this.getStone = () => EMPTY;
    this.lastMove = null;
    this.forbidden = [];
    this.winLine = null;
    this.interactive = false;
    this.isForbiddenCell = () => false;
    this.hover = null;
    this.anim = null;

    canvas.addEventListener('mousemove', (e) => this.onMove(e));
    canvas.addEventListener('mouseleave', () => {
      if (this.hover) { this.hover = null; this.render(); }
      this.canvas.style.cursor = 'default';
    });
    canvas.addEventListener('click', (e) => {
      const c = this.cellAt(e);
      if (c && this.onClick) this.onClick(c.x, c.y);
    });
    if (typeof ResizeObserver !== 'undefined') {
      new ResizeObserver(() => this.resize()).observe(canvas.parentElement);
    }
    this.resize();
  }

  setState({ size, getStone, lastMove, forbidden, winLine, interactive, isForbiddenCell, currentColor, moveNumbers, animate }) {
    this.size = size;
    this.getStone = getStone || (() => EMPTY);
    this.lastMove = lastMove || null;
    this.forbidden = forbidden || [];
    this.winLine = winLine || null;
    this.interactive = !!interactive;
    this.isForbiddenCell = isForbiddenCell || (() => false);
    this.currentColor = currentColor || BLACK;
    this.moveNumbers = moveNumbers || null;
    if (animate && this.lastMove) {
      this.anim = { x: this.lastMove.x, y: this.lastMove.y, start: performance.now() };
    } else {
      this.anim = null;
    }
    this.resize();
    if (this.anim) this.tick();
  }

  resize() {
    const parent = this.canvas.parentElement;
    const css = Math.max(300, Math.min(parent.clientWidth || 600, parent.clientHeight || 600));
    const dpr = window.devicePixelRatio || 1;
    this.css = css;
    this.canvas.style.width = css + 'px';
    this.canvas.style.height = css + 'px';
    this.canvas.width = Math.round(css * dpr);
    this.canvas.height = Math.round(css * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.margin = css * 0.03 + 16;
    this.cell = (css - this.margin * 2) / (this.size - 1);
    this.render();
  }

  px(i) { return this.margin + i * this.cell; }

  cellAt(e) {
    const rect = this.canvas.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    const i = Math.round((mx - this.margin) / this.cell);
    const j = Math.round((my - this.margin) / this.cell);
    if (i < 0 || j < 0 || i >= this.size || j >= this.size) return null;
    if (Math.abs(mx - this.px(i)) > this.cell * 0.47) return null;
    if (Math.abs(my - this.px(j)) > this.cell * 0.47) return null;
    return { x: i, y: j };
  }

  onMove(e) {
    const c = this.cellAt(e);
    let cursor = 'default';
    if (c) {
      if (this.interactive && this.getStone(c.x, c.y) === EMPTY) {
        cursor = this.isForbiddenCell(c) ? 'not-allowed' : 'pointer';
      } else {
        cursor = 'not-allowed'; // 非本轮落子（含已占点）
      }
    }
    this.canvas.style.cursor = cursor;
    const changed = (c && (!this.hover || this.hover.x !== c.x || this.hover.y !== c.y)) || (!c && this.hover);
    this.hover = c;
    if (changed) this.render();
  }

  tick() {
    if (!this.anim) return;
    const t = (performance.now() - this.anim.start) / 150;
    if (t >= 1) { this.anim = null; this.render(); return; }
    this.render();
    requestAnimationFrame(() => this.tick());
  }

  render() {
    const { ctx, css, size } = this;
    ctx.clearRect(0, 0, css, css);
    this.drawWood();
    this.drawGrid();
    if (this.winLine && this.winLine.length) this.drawWinLine();
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) {
        const v = this.getStone(x, y);
        if (v !== BLACK && v !== WHITE) continue;
        const px = this.px(x), py = this.px(y);
        this.drawStone(px, py, v, 1, 1);
        const n = this.moveNumbers && this.moveNumbers.get(y * size + x);
        if (n) this.drawNumber(px, py, n, v);
      }
    }
    this.drawForbidden();
    if (this.lastMove) this.drawLastMark();
    if (this.winLine && this.winLine.length) this.drawWinRings();
    this.drawGhost();
  }

  // 棋子上的手数标注：黑子白字、白子黑字，三位数自动缩小
  drawNumber(x, y, n, color) {
    const { ctx, cell } = this;
    const s = String(n);
    const fs = Math.max(9, cell * (s.length >= 3 ? 0.32 : 0.42));
    ctx.save();
    ctx.font = `600 ${fs}px ui-monospace, Menlo, Consolas, monospace`;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillStyle = color === BLACK ? 'rgba(255,255,255,0.92)' : 'rgba(25,25,25,0.85)';
    ctx.fillText(s, x, y + fs * 0.05);
    ctx.restore();
  }

  // 胜利连线：棋子上层的金环 + 柔光，确保高亮不被棋子遮挡
  drawWinRings() {
    const { ctx, cell } = this;
    ctx.save();
    ctx.strokeStyle = '#f2c14e';
    ctx.lineWidth = 2.5;
    ctx.shadowColor = 'rgba(242,193,78,0.85)';
    ctx.shadowBlur = 9;
    for (const p of this.winLine) {
      ctx.beginPath();
      ctx.arc(this.px(p.x), this.px(p.y), cell * 0.52, 0, Math.PI * 2);
      ctx.stroke();
    }
    ctx.restore();
  }

  drawWood() {
    const { ctx, css } = this;
    const g = ctx.createLinearGradient(0, 0, css, css);
    g.addColorStop(0, '#d9a95c');
    g.addColorStop(0.5, '#c9973f');
    g.addColorStop(1, '#b9852f');
    ctx.fillStyle = g;
    ctx.fillRect(0, 0, css, css);
    // 木纹
    ctx.save();
    ctx.globalAlpha = 0.10;
    ctx.strokeStyle = '#7a5a1e';
    ctx.lineWidth = 1;
    for (let i = 0; i < 46; i++) {
      const yy = (i / 46) * css + Math.sin(i * 2.7) * 4;
      ctx.beginPath();
      for (let x = 0; x <= css; x += 16) {
        const wob = Math.sin(x * 0.02 + i * 1.7) * 2.4 + Math.sin(x * 0.005 + i) * 3;
        if (x === 0) ctx.moveTo(x, yy + wob);
        else ctx.lineTo(x, yy + wob);
      }
      ctx.stroke();
    }
    ctx.restore();
    // 四周暗角
    const v = ctx.createRadialGradient(css / 2, css / 2, css * 0.35, css / 2, css / 2, css * 0.75);
    v.addColorStop(0, 'rgba(0,0,0,0)');
    v.addColorStop(1, 'rgba(60,30,0,0.28)');
    ctx.fillStyle = v;
    ctx.fillRect(0, 0, css, css);
  }

  drawGrid() {
    const { ctx, size, cell } = this;
    ctx.strokeStyle = 'rgba(62,40,8,0.75)';
    ctx.lineWidth = 1;
    for (let i = 0; i < size; i++) {
      const p = this.px(i) + 0.5;
      ctx.beginPath(); ctx.moveTo(this.px(0), p); ctx.lineTo(this.px(size - 1), p); ctx.stroke();
      ctx.beginPath(); ctx.moveTo(p, this.px(0)); ctx.lineTo(p, this.px(size - 1)); ctx.stroke();
    }
    ctx.lineWidth = 2;
    ctx.strokeRect(this.px(0), this.px(0), cell * (size - 1), cell * (size - 1));
    // 星位
    const q = Math.floor(size / 4);
    const stars = [[q, q], [q, size - 1 - q], [size - 1 - q, q], [size - 1 - q, size - 1 - q]];
    if (size % 2 === 1) stars.push([(size - 1) / 2, (size - 1) / 2]);
    ctx.fillStyle = 'rgba(62,40,8,0.8)';
    for (const [sx, sy] of stars) {
      ctx.beginPath();
      ctx.arc(this.px(sx), this.px(sy), Math.max(2.5, cell * 0.09), 0, Math.PI * 2);
      ctx.fill();
    }
    // 坐标（协议 0 基）
    ctx.fillStyle = 'rgba(62,40,8,0.6)';
    ctx.font = '10px ui-monospace, Menlo, monospace';
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    for (let i = 0; i < size; i++) {
      ctx.fillText(String(i), this.px(i), this.margin * 0.42);
      ctx.fillText(String(i), this.margin * 0.42, this.px(i));
    }
  }

  drawWinLine() {
    const { ctx, cell } = this;
    const a = this.winLine[0], b = this.winLine[this.winLine.length - 1];
    ctx.save();
    ctx.strokeStyle = 'rgba(242,193,78,0.5)';
    ctx.lineWidth = cell * 0.34;
    ctx.lineCap = 'round';
    ctx.beginPath();
    ctx.moveTo(this.px(a.x), this.px(a.y));
    ctx.lineTo(this.px(b.x), this.px(b.y));
    ctx.stroke();
    ctx.restore();
  }

  drawStone(x, y, color, alpha, scale) {
    const { ctx, cell } = this;
    const r = cell * 0.46 * scale;
    ctx.save();
    ctx.globalAlpha = alpha;
    ctx.shadowColor = 'rgba(40,20,0,0.45)';
    ctx.shadowBlur = 3;
    ctx.shadowOffsetY = 1.5;
    const g = ctx.createRadialGradient(x - r * 0.35, y - r * 0.4, r * 0.1, x, y, r);
    if (color === BLACK) {
      g.addColorStop(0, '#5a5a5c');
      g.addColorStop(0.55, '#1c1c1e');
      g.addColorStop(1, '#050506');
    } else {
      g.addColorStop(0, '#ffffff');
      g.addColorStop(0.6, '#efede6');
      g.addColorStop(1, '#c9c4b4');
    }
    ctx.fillStyle = g;
    ctx.beginPath();
    ctx.arc(x, y, r, 0, Math.PI * 2);
    ctx.fill();
    if (color === WHITE) {
      ctx.shadowColor = 'transparent';
      ctx.strokeStyle = 'rgba(80,60,30,0.35)';
      ctx.lineWidth = 0.8;
      ctx.stroke();
    }
    ctx.restore();
  }

  drawForbidden() {
    const { ctx, cell } = this;
    const l = cell * 0.2;
    ctx.save();
    ctx.strokeStyle = 'rgba(196,44,24,0.9)';
    ctx.lineWidth = 2;
    ctx.lineCap = 'round';
    ctx.shadowColor = 'rgba(196,44,24,0.5)';
    ctx.shadowBlur = 4;
    for (const p of this.forbidden) {
      const cx = this.px(p.x), cy = this.px(p.y);
      ctx.beginPath();
      ctx.moveTo(cx - l, cy - l); ctx.lineTo(cx + l, cy + l);
      ctx.moveTo(cx + l, cy - l); ctx.lineTo(cx - l, cy + l);
      ctx.stroke();
    }
    ctx.restore();
  }

  drawLastMark() {
    // 朱红圆环标记最后一手（棋子中央已显示手数，圆环不与之重叠）
    const { ctx, cell } = this;
    const { x, y } = this.lastMove;
    ctx.save();
    ctx.strokeStyle = '#e05a3a';
    ctx.lineWidth = 2;
    ctx.shadowColor = 'rgba(224,90,58,0.7)';
    ctx.shadowBlur = 5;
    ctx.beginPath();
    ctx.arc(this.px(x), this.px(y), cell * 0.5, 0, Math.PI * 2);
    ctx.stroke();
    ctx.restore();
  }

  drawGhost() {
    if (!this.interactive || !this.hover) return;
    const { x, y } = this.hover;
    if (this.getStone(x, y) !== EMPTY) return;
    if (this.isForbiddenCell({ x, y })) return;
    this.drawStone(this.px(x), this.px(y), this.currentColor || BLACK, 0.45, 1);
  }
}
