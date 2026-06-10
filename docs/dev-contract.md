# 开发契约（2026-05-29 迭代）— 唯一真相源

> 本文件是本轮 T1–T5 迭代中前端 / Go 后端 / Python 侧车三方共享的接口契约。
> 任何字段重命名必须三方同步。响应一律平铺 JSON。

## 0. 既有约定（不变）

- 数据库：SQLite `data/storage/papers.db`，schema 见 `backend/migrations/`（001_init – 003_filters_chunkmeta）
- 论文全文已落在 `papers_master.raw_body`（ingest 写入 `parse_docx` 的 `full_text`），**T1 直接复用，无需新增列**
- 向量：base64(float32 LE) 存 `paper_chunks.embedding`；Go `store.EncodeVector/DecodeVector` 与 Python `vector_codec` 对称
- LLM：Claude Opus 4.8（`claude-opus-4-8`，OpenAI 兼容中转端点，env `LLM_BASE_URL/LLM_MODEL/LLM_API_KEY`）；嵌入：本地 `BAAI/bge-small-zh-v1.5`
- 端口（远端部署）：Go 19080 / Py 19001 / Vite 19173；本地默认 8080 / 8001 / 5173

## 1. 既有接口（保持，仅 GetPaper 增字段）

| 方法 | 路径 | 备注 |
| --- | --- | --- |
| GET | `/api/health` | `{status}` |
| GET | `/api/stats` | `{paper_count,chunk_count,year_dist,top_journals[].name}` |
| POST | `/api/search/traditional` | 请求 `{q,field,year,page,page_size,sort,filters?:{authors[],affiliations[],journals[],clcs[],years[],year_from,year_to,is_core}}`（q 支持 AND/OR/NOT/括号/"短语"/字段限定）→ `{hits,total,facets}` |
| POST | `/api/search/smart` | 请求 `{q,filters?}` → `{golden,rewrite,boolean_query,facets,list_bm25,list_vector}`（rewrite.filter_conditions 扩展至 authors/affiliation/journal/is_core/clc_number/publish_year/year_from/year_to） |
| POST | `/api/analyze/generate` | **现有综述链路，Prompt 不动** 请求 `{q,paper_ids}` → `{answer,citations}` |
| GET | `/api/papers/{id}` | **新增 `full_text` 字段**（= raw_body）。其余平铺字段不变 |
| GET | `/api/papers/{id}/chunks?q=` | `{chunks[]}` |
| GET | `/api/history?limit=` | `{history[]}` |
| POST | `/api/analyze/run` | `{kind,params}` → `{data}` |
| POST | `/api/reload` | `{status}` |

## 2. 新增接口（本轮）

### T4 · 智能问答 RAG-QA

**Go** `POST /api/qa/answer`
- 请求：`{ "question": string, "filters"?: {publish_year?:int, author?:string, journal?:string} }`
- 流程：复用 smart 检索（rewrite→BM25∪向量→RRF）拿 golden；证据选择：默认 Top5；若第 3 名之后分数断崖（次名 < 前一名 * 0.5）或候选 < 5，则取 Top3 并标记证据不足。对每篇取最高分 chunk + research_design_text 组上下文，调 Py `/qa`。
- 响应：
```json
{
  "answer": "string（固定五段式 Markdown：## 概念解释/背景说明/方法依据/经验证据/研究空白，正文带 chunk 级 [n] 行内引用）",
  "evidence_sufficient": true,
  "references": [
    {"rank":1,"paper_id":"...","title":"...","author":"...","year":2024,
     "doi":"...","journal":"...","matched_by":"关键词|语义|关键词+语义",
     "score":0.83,"snippet":"证据片段（可截断）"}
  ],
  "citations": [
    {"n":1,"chunk_id":"...","paper_id":"...","chunk_text":"...",
     "chapter_title":"...","section_role":"...","title":"...","doi":"..."}
  ]
}
```

**Python** `POST /qa`
- 请求：`{ "question": string, "papers": [GeneratePaper], "evidence_sufficient": bool }`
  （GeneratePaper 同 /generate：paper_id,title,doi,author,keywords,abstract,publish_year,research_design_text,top_chunk_text,relevance_score）
- **新 QA Prompt（独立于综述 Prompt）**：只基于证据回答；无证据必须答"证据不足，无法回答"；输出结构 = 固定五段（## 概念解释/背景说明/方法依据/经验证据/研究空白）+ chunk 级 [n] 行内引用 + 每段末「参考文献」行；某段无证据时只写「暂无相关文献证据」。`evidence_sufficient=false` 时在开头提示可信度有限。
- 响应：`{ "answer": string }`（references 由 Go 侧用入参 papers 组装，Py 只负责生成 answer）

### T3 · 文献综述双模式（复用现有综述 Prompt，不改）

**Go** `POST /api/review/auto`
- 请求：`{ "q": string }` → 走 smart 检索取 Top5 → 调 Py `/generate`（现有综述 Prompt）
- 响应：`{ "answer": string, "citations": [...] }`（同 /analyze/generate）

**Go** `POST /api/review/manual`
- 请求：`{ "doi"?: string, "title"?: string, "text"?: string }`（精确定位：doi 全等 / title 全等匹配库内单篇；text 为直接粘贴的全文）
- 流程：精确取 1 篇（或 text 直接作为单篇证据）→ 调 Py `/generate`
- 响应：`{ "answer": string, "citations": [...], "matched": {paper_id,title} | null }`
- 错误：精确定位失败返回 404 `{"error":"未能精确定位到库内文献"}`

> 上传文件解析（.pdf/.docx/.txt）本轮：前端只接 `.txt/.docx` 文本，前端读出纯文本后走 `text` 字段；不在后端做文件解析。

### T5 · 论文详情页智能体

**Go** `POST /api/papers/{id}/chat`
- 请求：`{ "question": string }` → 取该论文全字段（含 full_text）调 Py `/chat`
- 响应：`{ "answer": string, "evidence_snippets": [string] }`

**Go** `POST /api/papers/{id}/summary`
- 请求：空 → 调 Py `/summary`
- 响应：`{ "summary": string, "method": string, "result": string, "keywords": [string] }`

**Go** `POST /api/papers/{id}/mindmap`
- 请求：空 → 调 Py `/mindmap`
- 响应：`{ "markdown": string }`（markmap Markdown 大纲，不含 ``` 围栏，前端用 markmap 渲染）

**Go** `POST /api/papers/{id}/related`
- 纯 Go 实现，不调 LLM：seed_keywords = 该论文 keywords 分词；seed = title+keywords+abstract 的向量（用该论文已存的最高信息 chunk 向量或对 abstract 现算）。BM25(keywords) ∪ 向量(abstract) → RRF → 去掉自身 → TopN=20。
- 响应：`{ "related_papers": [ {paper_id,title,author,year,doi,journal,score,matched_by} ] }`

**Python 新增**：`POST /chat`、`POST /summary`、`POST /mindmap`
- 入参都带完整论文上下文：`{paper:{paper_id,title,author,doi,publish_year,journal,keywords,abstract,research_design_text,full_text}, question?}`
- 各自独立 Prompt。summary 用 prompt 工程产出 summary/method/result/keywords[5-10]。mindmap 产出 markmap Markdown 大纲。chat 只基于该论文作答，越界则答"论文未提供"。

## 3. 前端类型（TS）务必与上面平铺字段逐一对齐

- `Paper` 增 `full_text: string`
- 新增 `QaResponse{answer,evidence_sufficient,references:Reference[],citations:ChunkCitation[]}`、`Reference{rank,paper_id,title,author,year,doi,journal,matched_by,score,snippet}`、`ChunkCitation{n,chunk_id,paper_id,chunk_text,chapter_title,section_role,...}`
- 新增 `ReviewResponse{answer,citations:Citation[],matched?:{paper_id,title}|null}`
- 新增 `SummaryResponse{summary,method,result,keywords:string[]}`
- 新增 `MindmapResponse{markdown}`
- 新增 `ChatResponse{answer,evidence_snippets:string[]}`
- 新增 `RelatedResponse{related_papers:RelatedPaper[]}`
