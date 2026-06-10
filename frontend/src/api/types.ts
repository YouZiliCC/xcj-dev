// ============================================================
// API types — 严格对齐 docs/dev-contract.md 平铺字段
// ============================================================

export interface HealthResponse {
  status: string;
}

export interface JournalCount {
  name: string;
  count: number;
}

export interface StatsResponse {
  paper_count: number;
  chunk_count: number;
  year_dist: Record<string, number>;
  top_journals: JournalCount[];
}

// ---------------- Search ----------------
export type SearchField =
  | "all"
  | "theme"
  | "title_or_keywords"
  | "title"
  | "first_author"
  | "author"
  | "affiliation"
  | "keywords"
  | "abstract"
  | "doi";

// 知网式硬过滤：同字段多值 OR，字段之间 AND
export interface SearchFilters {
  authors?: string[];
  affiliations?: string[];
  journals?: string[];
  clcs?: string[];
  years?: number[];
  year_from?: number;
  year_to?: number;
  is_core?: boolean;
}

export interface FacetItem {
  value: string;
  count: number;
}

// 侧边栏聚合：每个过滤字段的候选值 + 命中数（降序）
export interface Facets {
  authors: FacetItem[];
  journals: FacetItem[];
  years: FacetItem[];
  core: FacetItem[];
  clc: FacetItem[];
  affiliations: FacetItem[];
}

export interface TraditionalSearchRequest {
  q?: string;
  field?: SearchField;
  year?: number;
  page?: number;
  page_size?: number;
  sort?: "relevance" | "year";
  filters?: SearchFilters;
}

export interface Hit {
  paper_id: string;
  score: number;
  matched_fields: string[];
  rank: number;
  title: string;
  author: string;
  year: number;
  journal: string;
  affiliation: string;
  abstract_preview: string;
  keywords: string;
}

export interface TraditionalSearchResponse {
  hits: Hit[];
  total: number;
  facets?: Facets;
  boolean_mode?: boolean;
}

// ---------------- Smart search ----------------
export interface FilterConditions {
  publish_year?: number | null;
  author?: string | null;
  journal?: string | null;
  [extra: string]: unknown;
}

export interface SearchPayload {
  core_semantic_sentence: string;
  academic_keywords: string[];
  synonyms_and_extensions: string[];
  potential_variables: string[];
  research_design_terms: string[];
}

export interface Rewrite {
  filter_conditions: FilterConditions;
  boolean_query?: string;
  search_payload: SearchPayload;
}

export interface SmartSearchRequest {
  q: string;
  filters?: SearchFilters;
}

export interface SmartSearchResponse {
  golden: Hit[];
  rewrite: Rewrite;
  boolean_query?: string;
  facets?: Facets;
  list_bm25: Hit[];
  list_vector: Hit[];
}

// ---------------- Analyze (现有综述链路) ----------------
export interface AnalyzeGenerateRequest {
  q: string;
  paper_ids: string[];
}

export interface Citation {
  paper_id: string;
  title: string;
  doi?: string;
  author: string;
  keywords: string;
  abstract: string;
  publish_year: number;
  relevance_score: number;
  top_chunk_text?: string;
  chunk_text?: string;
}

export interface AnalyzeGenerateResponse {
  answer: string;
  citations: Citation[];
}

// ---------------- Paper ----------------
export interface Chunk {
  chunk_id: string;
  chunk_index: number;
  paragraph_index: number;
  offset_start: number;
  chunk_text: string;
  chapter_title?: string;
  chapter_index?: number;
  section_role?: string; // 概念解释/背景说明/方法依据/经验证据/研究空白/其他
  tag_confidence?: string;
  split_method?: string;
}

export interface Paper {
  paper_id: string;
  title: string;
  author: string;
  publish_year: number;
  source_journal: string;
  affiliation?: string;
  doi?: string;
  abstract: string;
  keywords: string;
  core_type?: string;
  is_core?: number;
  clc_number?: string;
  research_design_text?: string;
  full_text: string;
  chunks: Chunk[];
}

// ---------------- History ----------------
export interface HistoryItem {
  id: number;
  mode: string;
  query: string;
  filters: string;
  created_at: string;
}

// ---------------- Analyze run ----------------
export type AnalyzeKind =
  | "year"
  | "authors"
  | "keywords"
  | "journals"
  | "cooccurrence"
  | "tfidf";

export interface AnalyzeRunRequest {
  kind: AnalyzeKind;
  params?: Record<string, unknown>;
}

export interface AnalyzeRunResponse {
  data: unknown;
}

// ============================================================
// 本轮新增 (T3 / T4 / T5)
// ============================================================

// ---------------- T4 · 智能问答 ----------------
export interface QaFilters extends SearchFilters {
  publish_year?: number;
  author?: string;
  journal?: string;
}

export interface QaRequest {
  question: string;
  filters?: QaFilters;
}

// 细粒度文本块引用（五段式回答的 [n] 对应项）
export interface ChunkCitation {
  ref_id: number;
  chunk_id: string;
  chunk_index: number;
  paper_id: string;
  title: string;
  author: string;
  year: number;
  journal: string;
  doi: string;
  chapter_title: string;
  section_role: string;
  text: string;
}

export interface Reference {
  rank: number;
  paper_id: string;
  title: string;
  author: string;
  year: number;
  doi: string;
  journal: string;
  matched_by: string; // 关键词 | 语义 | 关键词+语义
  score: number;
  snippet: string;
}

export interface QaResponse {
  answer: string;
  evidence_sufficient: boolean;
  references: Reference[];
}

// ---------------- T3 · 文献综述 ----------------
export interface ReviewAutoRequest {
  q: string;
}

export interface ReviewManualRequest {
  doi?: string;
  title?: string;
  text?: string;
  author?: string;
  year?: number;
  journal?: string;
}

export interface ReviewMatched {
  paper_id: string;
  title: string;
}

export interface ReviewResponse {
  answer: string;
  citations: Citation[];
  matched?: ReviewMatched | null;
}

// ---------------- 流式事件 meta 负载（SSE） ----------------
export interface ReviewMeta {
  citations: Citation[];
  matched?: ReviewMatched | null;
}

export interface QaMeta {
  evidence_sufficient: boolean;
  references: Reference[];
  citations?: ChunkCitation[];
}

export interface ChatMeta {
  evidence_snippets: string[];
}

// ---------------- T5 · 论文详情智能体 ----------------
export interface ChatRequest {
  question: string;
}

export interface ChatResponse {
  answer: string;
  evidence_snippets: string[];
}

export interface SummaryResponse {
  summary: string;
  method: string;
  result: string;
  keywords: string[];
}

export interface MindmapResponse {
  markdown: string;
}

export interface RelatedPaper {
  paper_id: string;
  title: string;
  author: string;
  year: number;
  doi: string;
  journal: string;
  score: number;
  matched_by: string;
}

export interface RelatedResponse {
  related_papers: RelatedPaper[];
}
