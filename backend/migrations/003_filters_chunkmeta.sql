-- 003: 知网式硬过滤字段（papers_master）+ 两阶段切分元数据（paper_chunks）。
-- Migrate 容忍 duplicate column 错误，因此每列一条 ALTER 即可幂等。
ALTER TABLE papers_master ADD COLUMN core_type TEXT;
ALTER TABLE papers_master ADD COLUMN is_core INTEGER;
ALTER TABLE papers_master ADD COLUMN clc_number TEXT;
ALTER TABLE paper_chunks ADD COLUMN chapter_title TEXT;
ALTER TABLE paper_chunks ADD COLUMN chapter_index INTEGER;
ALTER TABLE paper_chunks ADD COLUMN section_role TEXT;
ALTER TABLE paper_chunks ADD COLUMN tag_confidence TEXT;
ALTER TABLE paper_chunks ADD COLUMN split_method TEXT
