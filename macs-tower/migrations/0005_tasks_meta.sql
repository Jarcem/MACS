-- V3.5.5：任务验收标准与元数据（数据库设计 §2.2 列级口径）
ALTER TABLE tasks ADD COLUMN priority               TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE tasks ADD COLUMN acceptance_criteria    TEXT NOT NULL DEFAULT '[]';   -- JSONB 兼容：验收项数组
ALTER TABLE tasks ADD COLUMN acceptance_design_note TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN deadline_at            TEXT;                          -- ISO8601（SQLite TEXT 兼容）
