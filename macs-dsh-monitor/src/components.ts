// 终端共享组件（UI 设计 V1.4 §16.1）

// ASCII 进度条：'██████░░░░' 形态，width 格宽
export function bar(pct: number, width = 16): string {
  const clamped = Math.max(0, Math.min(100, Math.round(pct)));
  const filled = Math.round((clamped / 100) * width);
  return '█'.repeat(filled) + '░'.repeat(width - filled);
}

// 任务级进度合成（加权公式 §16.1，默认 w=1）
export interface ProgressNode {
  status: string;
  progress_percent?: number | null;
}

export function taskProgress(nodes: ProgressNode[]): number {
  if (nodes.length === 0) return 0;
  const p = (n: ProgressNode): number => {
    if (n.status === 'completed') return 1;
    if (n.status === 'running') return (n.progress_percent ?? 0) / 100;
    if (n.status === 'self_checking' || n.status === 'reviewing') return 0.9;
    return 0;
  };
  return Math.round((100 * nodes.reduce((s, n) => s + p(n), 0)) / nodes.length);
}
