// 棋局规则：胜负判定 + 连珠禁手（RIF 9.3 递归口径，递归深度封顶近似）。
// 约定：黑永远先行；board 与 moves 保持一致；禁手分析在"候选子已在盘中"的
// 局面上进行（与 RIF"先在脑中落子再推演"的定义一致）。
//
// 禁手口径（与引擎 rules.go 一致并加做 RIF 递归）：
//   - 成恰五优先于一切禁手形状；
//   - 长连（≥6）本身是禁手；
//   - 方向内枚举 4 步内的空点试填：填后过候选点的连长恰为 5 → 计一个"四"；
//     恰为 4 且两端皆空（活四）→ 计一个"三"；按参与棋子集合去重，
//     因此活四的两端属于同一个四（保证"三可成活四"的定义自洽）；
//   - 三的成活点本身不得是禁手点（递归校验，深度封顶 6，深层按不禁处理）；
//   - 四的成必五点不可能是禁手点（恰五永不禁），故四无需递归。
//   - 同向双形（如 .XX_P_XX. 类）由去重掩码自然计为两个四。

export const EMPTY = 0;
export const BLACK = 1;
export const WHITE = 2;

export const FOUL_NAME = {
  'overline': '长连禁手（六连及以上）',
  'double-four': '四四禁手',
  'double-three': '三三禁手',
};

const DIRS = [[1, 0], [0, 1], [1, 1], [1, -1]];
const MAX_DEPTH = 6;

export class Game {
  constructor(size, renju) {
    this.size = size;
    this.renju = renju;
    this.board = new Int8Array(size * size);
    this.moves = [];      // {x, y, player, sent}  sent: 该手是否已告知引擎
    this.winner = null;   // BLACK | WHITE | 'draw'
    this.winReason = '';
    this.winLine = null;  // 胜利连线格子 [{x,y}...]
  }

  inB(x, y) { return x >= 0 && y >= 0 && x < this.size && y < this.size; }
  at(x, y) { return this.inB(x, y) ? this.board[y * this.size + x] : -1; }
  get blackToMove() { return this.moves.length % 2 === 0; }
  get current() { return this.blackToMove ? BLACK : WHITE; }
  countBy(player) {
    let c = 0;
    for (const m of this.moves) if (m.player === player) c++;
    return c;
  }
  lastMove() { return this.moves.length ? this.moves[this.moves.length - 1] : null; }

  // 落子（当前轮次方）并立即判定。sent 表示该手是否已告知引擎。
  place(x, y, sent) {
    const player = this.current;
    this.board[y * this.size + x] = player;
    this.moves.push({ x, y, player, sent: !!sent });
    return this.judge(x, y, player);
  }

  judge(x, y, player) {
    let maxRun = 0, maxCells = null;
    for (const [dx, dy] of DIRS) {
      const r = this.runFrom(x, y, dx, dy, player);
      if (r.len > maxRun) { maxRun = r.len; maxCells = r.cells; }
    }
    if (player === BLACK && this.renju) {
      if (maxRun === 5) return this.finish(BLACK, '五连达成', maxCells);
      if (maxRun >= 6) return this.finish(WHITE, '黑方长连犯规（六连及以上）', maxCells);
      const foul = this.forbiddenAt(x, y);
      if (foul) return this.finish(WHITE, `黑方${FOUL_NAME[foul]}`, null);
    } else if (maxRun >= 5) {
      return this.finish(player, '五连达成', maxCells);
    }
    if (this.moves.length === this.size * this.size) {
      return this.finish('draw', '棋盘已满，双方均无胜负', null);
    }
    if (this.renju && this.current === BLACK && !this.hasLegalPoint()) {
      return this.finish(WHITE, '黑方无合法落子点', null);
    }
    return { over: false };
  }

  hasLegalPoint() {
    for (let y = 0; y < this.size; y++) {
      for (let x = 0; x < this.size; x++) {
        if (this.at(x, y) !== EMPTY) continue;
        if (!this.forbiddenAt(x, y)) return true;
      }
    }
    return false;
  }

  finish(winner, reason, line) {
    this.winner = winner;
    this.winReason = reason;
    this.winLine = line;
    return { over: true, winner, reason };
  }

  runFrom(x, y, dx, dy, player) {
    const cells = [{ x, y }];
    for (let i = 1; ; i++) {
      if (this.at(x + dx * i, y + dy * i) !== player) break;
      cells.push({ x: x + dx * i, y: y + dy * i });
    }
    for (let i = 1; ; i++) {
      if (this.at(x - dx * i, y - dy * i) !== player) break;
      cells.unshift({ x: x - dx * i, y: y - dy * i });
    }
    return { len: cells.length, cells };
  }

  // 禁手判定：点为空时按"假设黑落于此"分析；点已是黑子时直接分析。
  // 返回 null | 'overline' | 'double-four' | 'double-three'。非连珠或白子永远 null。
  forbiddenAt(x, y, depth = 0) {
    if (!this.renju) return null;
    const v = this.at(x, y);
    if (v === WHITE) return null;
    const placed = v === BLACK;
    if (!placed) this.board[y * this.size + x] = BLACK;
    try {
      return this._forbidden(x, y, depth);
    } finally {
      if (!placed) this.board[y * this.size + x] = EMPTY;
    }
  }

  _forbidden(x, y, depth) {
    let maxRun = 0;
    for (const [dx, dy] of DIRS) {
      const r = this.runFrom(x, y, dx, dy, BLACK);
      if (r.len > maxRun) maxRun = r.len;
    }
    if (maxRun === 5) return null;        // 成五：五优先于禁手
    if (maxRun >= 6) return 'overline';   // 长连
    if (depth >= MAX_DEPTH) return null;  // 递归封顶：按不禁处理
    let fours = 0, threes = 0;
    for (const [dx, dy] of DIRS) {
      const { fours: f, threes: t } = this._lineShapes(x, y, dx, dy, depth);
      fours += f;
      threes += t;
      if (fours >= 2) return 'double-four';
      if (threes >= 2) return 'double-three';
    }
    return null;
  }

  // 沿 (dx,dy) 枚举候选点 4 步内的空点 e 试填，按过候选点的连长分类计数。
  _lineShapes(x, y, dx, dy, depth) {
    let fours = 0, threes = 0;
    const seenF = new Set(), seenT = new Set();
    for (let k = -4; k <= 4; k++) {
      if (k === 0) continue;
      const ex = x + dx * k, ey = y + dy * k;
      if (!this.inB(ex, ey) || this.at(ex, ey) !== EMPTY) continue;
      this.board[ey * this.size + ex] = BLACK;
      const r = this.runFrom(x, y, dx, dy, BLACK);
      const includesE = r.cells.some((c) => c.x === ex && c.y === ey);
      if (r.len === 5 && includesE) {
        const key = this._coreMask(r.cells, ex, ey, x, y, dx, dy);
        if (!seenF.has(key)) { seenF.add(key); fours++; }
      } else if (r.len === 4 && includesE) {
        const head = r.cells[0], tail = r.cells[r.cells.length - 1];
        if (this.at(head.x - dx, head.y - dy) === EMPTY && this.at(tail.x + dx, tail.y + dy) === EMPTY) {
          // RIF 递归：成活点 e 本身不得是禁手点（局面含候选子）
          const eFoul = this._forbidden(ex, ey, depth + 1);
          if (!eFoul) {
            const key = this._coreMask(r.cells, ex, ey, x, y, dx, dy);
            if (!seenT.has(key)) { seenT.add(key); threes++; }
          }
        }
      }
      this.board[ey * this.size + ex] = EMPTY;
    }
    return { fours, threes };
  }

  // 把连中棋子相对候选点的有向偏移编成掩码（排除试填点 e），用于同形去重。
  _coreMask(cells, ex, ey, x, y, dx, dy) {
    let mask = 0;
    for (const c of cells) {
      if (c.x === ex && c.y === ey) continue;
      const j = (c.x - x) * dx + (c.y - y) * dy;
      mask |= 1 << (j + 4);
    }
    return mask;
  }

  // 当前黑方所有禁手点（用于棋盘标记）。
  allForbiddenPoints() {
    const list = [];
    if (!this.renju || this.winner || this.current !== BLACK) return list;
    for (let y = 0; y < this.size; y++) {
      for (let x = 0; x < this.size; x++) {
        if (this.at(x, y) !== EMPTY) continue;
        if (this.forbiddenAt(x, y)) list.push({ x, y });
      }
    }
    return list;
  }
}
