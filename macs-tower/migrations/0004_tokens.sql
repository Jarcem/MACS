-- V3.5.3：每孵化器独立注册令牌（§6.4.16 rotate-token 吊销/轮换）
-- 空 = 该孵化器仅受全局 MACS_REGISTER_TOKEN 管控；非空 = 全局令牌或本行令牌任一命中即放行
ALTER TABLE incubators ADD COLUMN registration_token_hash TEXT NOT NULL DEFAULT '';
