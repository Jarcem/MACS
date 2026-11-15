-- V3.5.9：会话载体亲和（§6.4.9 CLI Native 扩展）——
-- 任务首次在会话载体上执行后记录 carrier，同任务后续子 Worker 在同一 session 内孵化
ALTER TABLE tasks ADD COLUMN carrier_incubator TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN carrier_session   TEXT NOT NULL DEFAULT '';
