-- 添加分组级模型拒绝列表。
-- 规则为 JSON 数组，例如：["claude-fable-5-1", "gpt-4o*"]。
ALTER TABLE groups
ADD COLUMN IF NOT EXISTS blocked_model_patterns JSONB NOT NULL
DEFAULT '[]'::jsonb;

COMMENT ON COLUMN groups.blocked_model_patterns IS
  '分组禁止使用的模型模式，支持 * 通配符；空数组表示不限制';
