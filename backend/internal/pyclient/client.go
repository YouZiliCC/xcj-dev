package pyclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *Client) postJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return fmt.Errorf("py %s status=%d body=%s", path, resp.StatusCode, preview)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		preview := string(raw)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return fmt.Errorf("decode %s: %w body=%s", path, err, preview)
	}
	return nil
}

// StreamNDJSON POST 到 path 并逐行读取 NDJSON 流：
//   {"delta":"..."} → 调 onDelta；{"done":true} → 结束；{"error":"..."} → 返回错误。
// 供 review/qa/chat 的流式生成复用，Go 上层再封装为 SSE 发给浏览器。
func (c *Client) StreamNDJSON(ctx context.Context, path string, payload any, onDelta func(string)) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("call %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return fmt.Errorf("py %s status=%d body=%s", path, resp.StatusCode, string(raw))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // 容纳较长的单块增量
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Delta string `json:"delta"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Error != "" {
			return fmt.Errorf("py %s stream error: %s", path, ev.Error)
		}
		if ev.Done {
			return nil
		}
		if ev.Delta != "" && onDelta != nil {
			onDelta(ev.Delta)
		}
	}
	return sc.Err()
}

// GenerateStream 流式生成文献综述。
func (c *Client) GenerateStream(ctx context.Context, req GenerateRequest, onDelta func(string)) error {
	return c.StreamNDJSON(ctx, "/generate_stream", req, onDelta)
}

// QAStream 流式生成智能问答回答。
func (c *Client) QAStream(ctx context.Context, question string, papers []GeneratePaper, evidenceSufficient bool, onDelta func(string)) error {
	req := map[string]any{
		"question":            question,
		"papers":              papers,
		"evidence_sufficient": evidenceSufficient,
	}
	return c.StreamNDJSON(ctx, "/qa_stream", req, onDelta)
}

// ChatStream 流式生成单篇论文 AI 同读回答。
func (c *Client) ChatStream(ctx context.Context, paper PaperContext, question string, onDelta func(string)) error {
	req := map[string]any{"paper": paper, "question": question}
	return c.StreamNDJSON(ctx, "/chat_stream", req, onDelta)
}

// Embed 调用 Python 服务获取嵌入向量。
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	req := map[string]any{"texts": texts}
	var resp struct {
		Vectors [][]float32 `json:"vectors"`
	}
	if err := c.postJSON(ctx, "/embed", req, &resp); err != nil {
		return nil, err
	}
	return resp.Vectors, nil
}

type RewriteResult struct {
	FilterConditions map[string]any `json:"filter_conditions"`
	BooleanQuery     string         `json:"boolean_query"`
	SearchPayload    struct {
		CoreSemanticSentence  string   `json:"core_semantic_sentence"`
		AcademicKeywords      []string `json:"academic_keywords"`
		SynonymsAndExtensions []string `json:"synonyms_and_extensions"`
		PotentialVariables    []string `json:"potential_variables"`
		ResearchDesignTerms   []string `json:"research_design_terms"`
	} `json:"search_payload"`
}

// Rewrite 让 LLM 拆解查询。
func (c *Client) Rewrite(ctx context.Context, query string) (*RewriteResult, error) {
	req := map[string]any{"q": query}
	var resp RewriteResult
	if err := c.postJSON(ctx, "/rewrite", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GenChunk 是带全局引用编号的取证文本块（QA 五段式细粒度引用用）。
type GenChunk struct {
	RefID        int     `json:"ref_id"`
	ChunkID      string  `json:"chunk_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Text         string  `json:"text"`
	SectionRole  string  `json:"section_role"`
	ChapterTitle string  `json:"chapter_title"`
	Score        float64 `json:"score"`
}

type GeneratePaper struct {
	PaperID            string     `json:"paper_id"`
	Title              string     `json:"title"`
	DOI                string     `json:"doi"`
	Author             string     `json:"author"`
	Keywords           string     `json:"keywords"`
	Abstract           string     `json:"abstract"`
	PublishYear        int        `json:"publish_year"`
	Journal            string     `json:"journal"`
	ResearchDesignText string     `json:"research_design_text"`
	TopChunkText       string     `json:"top_chunk_text"`
	RelevanceScore     float64    `json:"relevance_score"`
	Chunks             []GenChunk `json:"chunks,omitempty"`
}

type GenerateRequest struct {
	Query  string          `json:"query"`
	Papers []GeneratePaper `json:"papers"`
}

type GenerateResponse struct {
	Answer    string           `json:"answer"`
	Citations []map[string]any `json:"citations"`
}

// Generate 让 LLM 基于检索到的论文生成综述答案。
func (c *Client) Generate(ctx context.Context, req GenerateRequest) (*GenerateResponse, error) {
	var resp GenerateResponse
	if err := c.postJSON(ctx, "/generate", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PaperContext 是论文详情页智能体所需的完整论文上下文。
type PaperContext struct {
	PaperID            string `json:"paper_id"`
	Title              string `json:"title"`
	Author             string `json:"author"`
	DOI                string `json:"doi"`
	PublishYear        int    `json:"publish_year"`
	Journal            string `json:"journal"`
	Keywords           string `json:"keywords"`
	Abstract           string `json:"abstract"`
	ResearchDesignText string `json:"research_design_text"`
	FullText           string `json:"full_text"`
}

type SummaryResult struct {
	Summary  string   `json:"summary"`
	Method   string   `json:"method"`
	Result   string   `json:"result"`
	Keywords []string `json:"keywords"`
}

type ChatResult struct {
	Answer           string   `json:"answer"`
	EvidenceSnippets []string `json:"evidence_snippets"`
}

// QA 基于检索证据直接回答用户问题（非综述）。
func (c *Client) QA(ctx context.Context, question string, papers []GeneratePaper, evidenceSufficient bool) (string, error) {
	req := map[string]any{
		"question":            question,
		"papers":              papers,
		"evidence_sufficient": evidenceSufficient,
	}
	var resp struct {
		Answer string `json:"answer"`
	}
	if err := c.postJSON(ctx, "/qa", req, &resp); err != nil {
		return "", err
	}
	return resp.Answer, nil
}

// Summary 生成单篇论文的结构化概要。
func (c *Client) Summary(ctx context.Context, paper PaperContext) (*SummaryResult, error) {
	req := map[string]any{"paper": paper}
	var resp SummaryResult
	if err := c.postJSON(ctx, "/summary", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Mindmap 生成单篇论文的 markmap Markdown 大纲（前端渲染为真·思维导图）。
func (c *Client) Mindmap(ctx context.Context, paper PaperContext) (string, error) {
	req := map[string]any{"paper": paper}
	var resp struct {
		Markdown string `json:"markdown"`
	}
	if err := c.postJSON(ctx, "/mindmap", req, &resp); err != nil {
		return "", err
	}
	return resp.Markdown, nil
}

// Chat 基于单篇论文上下文回答用户问题。
func (c *Client) Chat(ctx context.Context, paper PaperContext, question string) (*ChatResult, error) {
	req := map[string]any{"paper": paper, "question": question}
	var resp ChatResult
	if err := c.postJSON(ctx, "/chat", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Analyze 通用分析入口。
func (c *Client) Analyze(ctx context.Context, kind string, params map[string]any) (map[string]any, error) {
	req := map[string]any{"kind": kind, "params": params}
	var resp map[string]any
	if err := c.postJSON(ctx, "/analyze", req, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}
