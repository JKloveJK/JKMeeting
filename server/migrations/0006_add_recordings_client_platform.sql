-- P5 不停机迁移演练（expand 阶段）：新增可空列，旧版本 API 不感知该列、照常读写。
-- contract（删列/改非空）必须等所有实例都升级到会写该列的版本后再单独执行，不在本文件内。
ALTER TABLE recordings ADD COLUMN IF NOT EXISTS client_platform TEXT;

COMMENT ON COLUMN recordings.client_platform IS 'P5 expand/contract 演练列：apple/android，历史数据回填 unknown';
