package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"xcjdev/backend/internal/pyclient"
	"xcjdev/backend/internal/search"
	"xcjdev/backend/internal/store"
)

type Handlers struct {
	DB     *store.DB
	Py     *pyclient.Client
	BMIdx  *search.Index
	Chunks []store.Chunk

	mu     sync.RWMutex
	papers map[string]store.Paper
}

// Reload 重新从 DB 拉取 papers、chunks 并重建 BM25 索引。
func (h *Handlers) Reload() error {
	papers, err := h.DB.AllPapers()
	if err != nil {
		return fmt.Errorf("load papers: %w", err)
	}
	chunks, err := h.DB.AllChunks()
	if err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	idx := search.Build(papers)
	pm := make(map[string]store.Paper, len(papers))
	for _, p := range papers {
		pm[p.PaperID] = p
	}
	h.mu.Lock()
	h.BMIdx = idx
	h.Chunks = chunks
	h.papers = pm
	h.mu.Unlock()
	log.Printf("[reload] papers=%d chunks=%d", len(papers), len(chunks))
	return nil
}

func (h *Handlers) snapshot() (*search.Index, []store.Chunk, map[string]store.Paper) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.BMIdx, h.Chunks, h.papers
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[json] encode error: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sseStart 切换响应为 SSE 并返回一个 send(event,data) 闭包。
// 一旦调用，状态码即固定为 200，因此必须在所有可能失败的前置步骤（检索等）之后再调用。
func sseStart(w http.ResponseWriter) (send func(event string, data any), ok bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 关闭 nginx 缓冲
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	send = func(event string, data any) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	return send, true
}

// pickSnippets 在 fullText 中找包含问题关键词的段落，作为 AI 同读的原文依据。
// 与 pyservice.main._pick_snippets 行为保持一致（流式路径下证据片段改由 Go 计算）。
func pickSnippets(fullText, question string, maxN int) []string {
	text := strings.TrimSpace(fullText)
	if text == "" || strings.TrimSpace(question) == "" {
		return nil
	}
	sep := func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '、', ' ', '\t', '\n', '\r', '　':
			return true
		}
		return false
	}
	var terms []string
	for _, t := range strings.FieldsFunc(question, sep) {
		if len([]rune(t)) >= 2 {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		matched := false
		for _, t := range terms {
			if strings.Contains(para, t) {
				matched = true
				break
			}
		}
		if matched {
			r := []rune(para)
			snip := para
			if len(r) > 300 {
				snip = string(r[:300]) + "…"
			}
			out = append(out, snip)
			if len(out) >= maxN {
				break
			}
		}
	}
	return out
}

// --- handlers ---

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handlers) Stats(w http.ResponseWriter, r *http.Request) {
	_, _, papers := h.snapshot()
	chunkCount, _ := h.DB.CountChunks()
	paperCount, _ := h.DB.CountPapers()
	yearDist := map[int]int{}
	journalDist := map[string]int{}
	for _, p := range papers {
		yearDist[p.PublishYear]++
		if strings.TrimSpace(p.SourceJournal) != "" {
			journalDist[p.SourceJournal]++
		}
	}
	type kv struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	topJournals := make([]kv, 0, len(journalDist))
	for n, c := range journalDist {
		topJournals = append(topJournals, kv{Name: n, Count: c})
	}
	sort.Slice(topJournals, func(i, j int) bool { return topJournals[i].Count > topJournals[j].Count })
	if len(topJournals) > 10 {
		topJournals = topJournals[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"paper_count":  paperCount,
		"chunk_count":  chunkCount,
		"year_dist":    yearDist,
		"top_journals": topJournals,
	})
}

type traditionalRequest struct {
	Q        string `json:"q"`
	Field    string `json:"field"` // all|theme|title_or_keywords|title|first_author|author|affiliation|keywords|abstract|doi
	Year     int    `json:"year"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
	Sort     string `json:"sort"`
}

type hitView struct {
	PaperID         string   `json:"paper_id"`
	Score           float64  `json:"score"`
	MatchedFields   []string `json:"matched_fields"`
	Rank            int      `json:"rank"`
	Title           string   `json:"title"`
	Author          string   `json:"author"`
	Year            int      `json:"year"`
	Journal         string   `json:"journal"`
	Affiliation     string   `json:"affiliation"`
	AbstractPreview string   `json:"abstract_preview"`
	Keywords        string   `json:"keywords"`
}

// fieldPlan 把前端字段标识映射为「BM25 文本字段掩码」或「SQL 元数据列」。
// isText=true 表示走 BM25（title/keywords/abstract/research_design/body）。
func fieldPlan(field string) (active [5]bool, isText bool, metaCol string) {
	switch strings.TrimSpace(field) {
	case "", "all": // 全部
		return search.AllFields, true, ""
	case "theme": // 主题 = 题名 + 关键词 + 摘要
		return [5]bool{true, true, true, false, false}, true, ""
	case "title_or_keywords": // 题名或关键词
		return [5]bool{true, true, false, false, false}, true, ""
	case "title": // 题名
		return [5]bool{true, false, false, false, false}, true, ""
	case "keywords": // 关键词
		return [5]bool{false, true, false, false, false}, true, ""
	case "abstract": // 摘要
		return [5]bool{false, false, true, false, false}, true, ""
	case "author": // 作者（任意作者）
		return [5]bool{}, false, "author"
	case "first_author": // 第一作者（作者串首位）
		return [5]bool{}, false, "first_author"
	case "affiliation": // 作者单位
		return [5]bool{}, false, "affiliation"
	case "doi":
		return [5]bool{}, false, "doi"
	default:
		return search.AllFields, true, ""
	}
}

func idsToHits(ids []string) []search.Hit {
	hits := make([]search.Hit, 0, len(ids))
	for i, id := range ids {
		hits = append(hits, search.Hit{PaperID: id, Score: 0, Rank: i + 1})
	}
	return hits
}

func abstractPreview(s string) string {
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "..."
	}
	return s
}

func (h *Handlers) toHitViews(hits []search.Hit, papers map[string]store.Paper) []hitView {
	out := make([]hitView, 0, len(hits))
	for _, hi := range hits {
		p, ok := papers[hi.PaperID]
		if !ok {
			continue
		}
		out = append(out, hitView{
			PaperID:         hi.PaperID,
			Score:           hi.Score,
			MatchedFields:   hi.MatchedFields,
			Rank:            hi.Rank,
			Title:           p.Title,
			Author:          p.Author,
			Year:            p.PublishYear,
			Journal:         p.SourceJournal,
			Affiliation:     p.Affiliation,
			AbstractPreview: abstractPreview(p.Abstract),
			Keywords:        p.Keywords,
		})
	}
	return out
}

// filterTraditional 按「年份 + 可选元数据列」过滤得到 allowed paper id 列表。
// 没有任何过滤条件时返回 (nil,true) 表示全放行。metaCol 为空表示不加元数据条件。
func (h *Handlers) filterTraditional(metaCol, q string, year int) ([]string, bool, error) {
	var conds []string
	var args []any
	if year > 0 {
		conds = append(conds, "publish_year = ?")
		args = append(args, year)
	}
	q = strings.TrimSpace(q)
	if metaCol != "" && q != "" {
		switch metaCol {
		case "first_author":
			// 第一作者：作者串以该名字开头（逗号分隔，首位即第一作者）
			conds = append(conds, "TRIM(author) LIKE ?")
			args = append(args, q+"%")
		case "doi":
			conds = append(conds, "doi LIKE ?")
			args = append(args, "%"+q+"%")
		case "author":
			conds = append(conds, "author LIKE ?")
			args = append(args, "%"+q+"%")
		case "affiliation":
			conds = append(conds, "affiliation LIKE ?")
			args = append(args, "%"+q+"%")
		}
	}
	if len(conds) == 0 {
		return nil, true, nil
	}
	sqlText := "SELECT paper_id FROM papers_master WHERE " + strings.Join(conds, " AND ")
	rows, err := h.DB.Query(sqlText, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	return ids, false, rows.Err()
}

func (h *Handlers) SearchTraditional(w http.ResponseWriter, r *http.Request) {
	var req traditionalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 || req.PageSize > 200 {
		req.PageSize = 20
	}
	idx, _, papers := h.snapshot()
	if idx == nil {
		writeError(w, http.StatusServiceUnavailable, "index not ready")
		return
	}

	active, isText, metaCol := fieldPlan(req.Field)
	allowedIDs, all, err := h.filterTraditional(metaCol, req.Q, req.Year)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "filter: "+err.Error())
		return
	}

	var hits []search.Hit
	if isText {
		// 文本字段：BM25（按字段掩码）。年份过滤通过白名单实现。
		if !all {
			idx.SetAllowed(allowedIDs)
		} else {
			idx.SetAllowed(nil)
		}
		tokens := search.Tokenize(req.Q)
		if len(tokens) > 0 {
			hits = idx.QueryBM25Fields(tokens, search.DefaultFieldWeights(), active, 200)
		} else if !all {
			// 无关键词但有年份过滤：返回过滤后的论文
			hits = idsToHits(allowedIDs)
		}
	} else {
		// 元数据字段（作者/第一作者/作者单位/DOI）：SQL 过滤结果即检索结果。
		if !all {
			hits = idsToHits(allowedIDs)
		}
	}

	if req.Sort == "year" {
		sort.SliceStable(hits, func(i, j int) bool {
			return papers[hits[i].PaperID].PublishYear > papers[hits[j].PaperID].PublishYear
		})
		for i := range hits {
			hits[i].Rank = i + 1
		}
	}
	total := len(hits)
	start := (req.Page - 1) * req.PageSize
	end := start + req.PageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	page := hits[start:end]
	views := h.toHitViews(page, papers)

	filters, _ := json.Marshal(map[string]any{
		"field": req.Field, "year": req.Year,
		"page": req.Page, "page_size": req.PageSize, "sort": req.Sort,
	})
	if err := h.DB.AddHistory("traditional", req.Q, string(filters)); err != nil {
		log.Printf("[history] add: %v", err)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hits":  views,
		"total": total,
	})
}

type smartRequest struct {
	Q string `json:"q"`
}

func collectStrings(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// applyFilterConditions 把 LLM 给的过滤条件应用到 papers，得到 allowed paper_id 列表与是否全放行。
func (h *Handlers) applyFilterConditions(fc map[string]any) (map[string]bool, []string, bool) {
	if len(fc) == 0 {
		return nil, nil, true
	}
	yearList := collectStrings(fc["publish_year"])
	authorList := collectStrings(fc["author"])
	journalList := collectStrings(fc["journal"])
	if v, ok := fc["source_journal"]; ok {
		journalList = append(journalList, collectStrings(v)...)
	}
	var years []int
	for _, y := range yearList {
		if n, err := strconv.Atoi(strings.TrimSpace(y)); err == nil {
			years = append(years, n)
		}
	}
	// 数字形式
	if y, ok := fc["publish_year"].(float64); ok {
		years = append(years, int(y))
	}
	if len(years) == 0 && len(authorList) == 0 && len(journalList) == 0 {
		return nil, nil, true
	}
	var conds []string
	var args []any
	if len(years) > 0 {
		placeholders := make([]string, len(years))
		for i, y := range years {
			placeholders[i] = "?"
			args = append(args, y)
		}
		conds = append(conds, "publish_year IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(authorList) > 0 {
		var parts []string
		for _, a := range authorList {
			parts = append(parts, "author LIKE ?")
			args = append(args, "%"+a+"%")
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	if len(journalList) > 0 {
		var parts []string
		for _, j := range journalList {
			parts = append(parts, "source_journal LIKE ?")
			args = append(args, "%"+j+"%")
		}
		conds = append(conds, "("+strings.Join(parts, " OR ")+")")
	}
	if len(conds) == 0 {
		return nil, nil, true
	}
	q := "SELECT paper_id FROM papers_master WHERE " + strings.Join(conds, " AND ")
	rows, err := h.DB.Query(q, args...)
	if err != nil {
		log.Printf("[smart] filter sql error: %v", err)
		return nil, nil, true
	}
	defer rows.Close()
	m := map[string]bool{}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			m[id] = true
			ids = append(ids, id)
		}
	}
	return m, ids, false
}

// smartRetrieval 是一次智能检索（rewrite→BM25∪向量→RRF）的产物。
type smartRetrieval struct {
	Golden     []search.Hit
	ListBM25   []search.Hit
	ListVector []search.Hit
	Rewrite    *pyclient.RewriteResult
}

// smartVectorFloor 智能检索向量臂的余弦相似度下限（bge-small-zh：相关内容通常 ≥0.65，弱相关 <0.6）。
const smartVectorFloor = 0.60

// sharedMeaningfulToken 判断两组 token 是否共享至少一个「实义词」(长度≥2 的 bigram / 英文词)，
// 用来校验 LLM 改写是否仍贴着原始查询（单个常见汉字不算，避免巧合命中）。
func sharedMeaningfulToken(a, b []string) bool {
	set := make(map[string]struct{}, len(a))
	for _, t := range a {
		if len([]rune(t)) >= 2 {
			set[t] = struct{}{}
		}
	}
	for _, t := range b {
		if len([]rune(t)) >= 2 {
			if _, ok := set[t]; ok {
				return true
			}
		}
	}
	return false
}

// runSmartRetrieval 复用 SearchSmart 的"rewrite→BM25∪向量→RRF 得 golden"逻辑，供 smart/qa/review-auto 复用。
func (h *Handlers) runSmartRetrieval(ctx context.Context, q string) (*smartRetrieval, error) {
	idx, chunks, _ := h.snapshot()
	if idx == nil {
		return nil, fmt.Errorf("index not ready")
	}

	rewrite, err := h.Py.Rewrite(ctx, q)
	if err != nil {
		log.Printf("[smart] rewrite failed, fallback: %v", err)
	}

	// 校验改写：若改写结果与原始查询无任何「实义词」(长度≥2 的 bigram / 英文词) 交集，
	// 判定为改写跑偏/幻觉（曾出现「企业信息安全风险评估」被改写成「甲苯光催化降解」的情况），
	// 丢弃改写、回退到原始查询，避免整榜返回不相关文献。
	if rewrite != nil {
		qTok := search.Tokenize(q)
		rwTok := search.Tokenize(rewrite.SearchPayload.CoreSemanticSentence + " " + strings.Join(rewrite.SearchPayload.AcademicKeywords, " "))
		if !sharedMeaningfulToken(qTok, rwTok) {
			log.Printf("[smart] rewrite rejected (no overlap with query): core=%q", rewrite.SearchPayload.CoreSemanticSentence)
			rewrite = nil
		}
	}

	// 过滤
	var allowedMap map[string]bool
	var allowedIDs []string
	allAllowed := true
	if rewrite != nil {
		allowedMap, allowedIDs, allAllowed = h.applyFilterConditions(rewrite.FilterConditions)
	}
	if allAllowed {
		idx.SetAllowed(nil)
	} else {
		idx.SetAllowed(allowedIDs)
	}

	// 关键词聚合：仅用 学术关键词 + 近义扩展，并始终保留原始查询作为锚点。
	// 不再混入 potential_variables / research_design_terms（如「平台化程度」这类泛化词），
	// 它们只贡献常见字符、是无关文献被召回的主因。
	var kwParts []string
	if rewrite != nil {
		kwParts = append(kwParts, rewrite.SearchPayload.AcademicKeywords...)
		kwParts = append(kwParts, rewrite.SearchPayload.SynonymsAndExtensions...)
	}
	kwParts = append(kwParts, q)
	tokens := search.Tokenize(strings.Join(kwParts, " "))
	listA := idx.QueryBM25(tokens, search.DefaultFieldWeights(), 200)

	// 向量：设相似度下限 smartVectorFloor，过滤弱相关 chunk（提高查准率）。
	var listB []search.Hit
	core := q
	if rewrite != nil && strings.TrimSpace(rewrite.SearchPayload.CoreSemanticSentence) != "" {
		core = rewrite.SearchPayload.CoreSemanticSentence
	}
	vecs, vecErr := h.Py.Embed(ctx, []string{core})
	if vecErr != nil {
		log.Printf("[smart] embed failed: %v", vecErr)
	} else if len(vecs) > 0 && len(vecs[0]) > 0 {
		chunkHits := search.TopKChunksByVector(chunks, vecs[0], allowedMap, smartVectorFloor, 200)
		listB = search.AggregateChunksToPapers(chunkHits, 200)
	}

	// 融合后做精度裁剪：尾部去 BM25-only + 断崖截断 + 上限 50。
	golden := search.RRF(listA, listB, 60, 0)
	golden = search.PrecisionFilter(golden, 12, 0.55, 50)
	return &smartRetrieval{Golden: golden, ListBM25: listA, ListVector: listB, Rewrite: rewrite}, nil
}

func (h *Handlers) SearchSmart(w http.ResponseWriter, r *http.Request) {
	var req smartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	q := strings.TrimSpace(req.Q)
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is empty")
		return
	}
	_, _, papers := h.snapshot()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	res, err := h.runSmartRetrieval(ctx, q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	// 写历史
	filtersJSON, _ := json.Marshal(map[string]any{"rewrite": res.Rewrite != nil})
	if err := h.DB.AddHistory("smart", q, string(filtersJSON)); err != nil {
		log.Printf("[history] add: %v", err)
	}

	trim := func(hits []search.Hit, n int) []search.Hit {
		if len(hits) > n {
			return hits[:n]
		}
		return hits
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"golden":      h.toHitViews(res.Golden, papers),
		"rewrite":     res.Rewrite,
		"list_bm25":   h.toHitViews(trim(res.ListBM25, 50), papers),
		"list_vector": h.toHitViews(trim(res.ListVector, 50), papers),
	})
}

type generateRequest struct {
	Q        string   `json:"q"`
	PaperIDs []string `json:"paper_ids"`
}

// buildGeneratePapers 按 paper_ids 取每篇最高分 chunk 组装 GeneratePaper。
// 用 q 的嵌入向量挑最相关 chunk；若 Python 嵌入不可用则优雅降级为最长 chunk。
// topN<=0 表示不截断。
func (h *Handlers) buildGeneratePapers(ctx context.Context, q string, paperIDs []string, topN int) []pyclient.GeneratePaper {
	ids := paperIDs
	if topN > 0 && len(ids) > topN {
		ids = ids[:topN]
	}

	var qVec []float32
	vecs, err := h.Py.Embed(ctx, []string{q})
	if err != nil {
		log.Printf("[generate] embed unavailable, fallback to length-based chunk: %v", err)
	} else if len(vecs) > 0 {
		qVec = vecs[0]
	}

	_, _, papersMap := h.snapshot()
	genPapers := make([]pyclient.GeneratePaper, 0, len(ids))
	for _, id := range ids {
		p, ok := papersMap[id]
		if !ok {
			pp, err := h.DB.GetPaper(id)
			if err != nil || pp == nil {
				continue
			}
			p = *pp
		}
		chunks, _ := h.DB.ChunksByPaper(id)
		bestScore := 0.0
		bestText := ""
		if len(qVec) > 0 {
			for _, c := range chunks {
				s := search.Cosine(c.Embedding, qVec)
				if s > bestScore {
					bestScore = s
					bestText = c.ChunkText
				}
			}
		}
		if bestText == "" {
			// fallback：取最长的 chunk
			for _, c := range chunks {
				if len(c.ChunkText) > len(bestText) {
					bestText = c.ChunkText
				}
			}
		}
		genPapers = append(genPapers, pyclient.GeneratePaper{
			PaperID:            p.PaperID,
			Title:              p.Title,
			DOI:                p.DOI,
			Author:             p.Author,
			Keywords:           p.Keywords,
			Abstract:           p.Abstract,
			PublishYear:        p.PublishYear,
			ResearchDesignText: p.ResearchDesignText,
			TopChunkText:       bestText,
			RelevanceScore:     bestScore,
		})
	}
	return genPapers
}

// citationsFromGenPapers 把 GeneratePaper 数组组装成 citations（同 /analyze/generate）。
func citationsFromGenPapers(genPapers []pyclient.GeneratePaper) []map[string]any {
	citations := make([]map[string]any, 0, len(genPapers))
	for _, p := range genPapers {
		citations = append(citations, map[string]any{
			"paper_id":        p.PaperID,
			"title":           p.Title,
			"author":          p.Author,
			"keywords":        p.Keywords,
			"abstract":        p.Abstract,
			"doi":             p.DOI,
			"publish_year":    p.PublishYear,
			"relevance_score": p.RelevanceScore,
			"top_chunk_text":  p.TopChunkText,
		})
	}
	return citations
}

func (h *Handlers) AnalyzeGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Q) == "" || len(req.PaperIDs) == 0 {
		writeError(w, http.StatusBadRequest, "q and paper_ids required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	genPapers := h.buildGeneratePapers(ctx, req.Q, req.PaperIDs, 5)

	resp, err := h.Py.Generate(ctx, pyclient.GenerateRequest{Query: req.Q, Papers: genPapers})
	if err != nil {
		writeError(w, http.StatusBadGateway, "generate: "+err.Error())
		return
	}

	out := map[string]any{
		"answer":       resp.Answer,
		"py_citations": resp.Citations,
		"citations":    citationsFromGenPapers(genPapers),
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) GetPaper(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	p, err := h.DB.GetPaper(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p == nil {
		writeError(w, http.StatusNotFound, "paper not found")
		return
	}
	chunks, err := h.DB.ChunksByPaper(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type chunkView struct {
		ChunkID        string `json:"chunk_id"`
		ChunkIndex     int    `json:"chunk_index"`
		ParagraphIndex int    `json:"paragraph_index"`
		OffsetStart    int    `json:"offset_start"`
		ChunkText      string `json:"chunk_text"`
	}
	cvs := make([]chunkView, 0, len(chunks))
	for _, c := range chunks {
		cvs = append(cvs, chunkView{
			ChunkID:        c.ChunkID,
			ChunkIndex:     c.ChunkIndex,
			ParagraphIndex: c.ParagraphIndex,
			OffsetStart:    c.OffsetStart,
			ChunkText:      c.ChunkText,
		})
	}
	// 把 paper 字段平铺，便于前端 typed 客户端按平面结构消费。
	writeJSON(w, http.StatusOK, map[string]any{
		"paper_id":             p.PaperID,
		"title":                p.Title,
		"doi":                  p.DOI,
		"publish_year":         p.PublishYear,
		"author":               p.Author,
		"keywords":             p.Keywords,
		"abstract":             p.Abstract,
		"source_journal":       p.SourceJournal,
		"affiliation":          p.Affiliation,
		"research_design_text": p.ResearchDesignText,
		"full_text":            p.RawBody,
		"chunks":               cvs,
	})
}

func (h *Handlers) GetPaperChunks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	chunks, err := h.DB.ChunksByPaper(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type chunkView struct {
		ChunkID        string  `json:"chunk_id"`
		ChunkIndex     int     `json:"chunk_index"`
		ParagraphIndex int     `json:"paragraph_index"`
		OffsetStart    int     `json:"offset_start"`
		ChunkText      string  `json:"chunk_text"`
		Score          float64 `json:"score,omitempty"`
	}
	if q == "" {
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].ChunkIndex < chunks[j].ChunkIndex })
		out := make([]chunkView, 0, len(chunks))
		for _, c := range chunks {
			out = append(out, chunkView{
				ChunkID: c.ChunkID, ChunkIndex: c.ChunkIndex,
				ParagraphIndex: c.ParagraphIndex, OffsetStart: c.OffsetStart, ChunkText: c.ChunkText,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"chunks": out})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	vecs, err := h.Py.Embed(ctx, []string{q})
	if err != nil || len(vecs) == 0 {
		writeError(w, http.StatusBadGateway, "embed: "+fmt.Sprintf("%v", err))
		return
	}
	qv := vecs[0]
	type scored struct {
		c store.Chunk
		s float64
	}
	arr := make([]scored, 0, len(chunks))
	for _, c := range chunks {
		arr = append(arr, scored{c: c, s: search.Cosine(c.Embedding, qv)})
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].s > arr[j].s })
	out := make([]chunkView, 0, len(arr))
	for _, x := range arr {
		out = append(out, chunkView{
			ChunkID: x.c.ChunkID, ChunkIndex: x.c.ChunkIndex,
			ParagraphIndex: x.c.ParagraphIndex, OffsetStart: x.c.OffsetStart,
			ChunkText: x.c.ChunkText, Score: x.s,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"chunks": out})
}

func (h *Handlers) History(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := h.DB.RecentHistory(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": rows})
}

type analyzeRunRequest struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params"`
}

func (h *Handlers) AnalyzeRun(w http.ResponseWriter, r *http.Request) {
	var req analyzeRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Kind) == "" {
		writeError(w, http.StatusBadRequest, "kind required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, err := h.Py.Analyze(ctx, req.Kind, req.Params)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- T4/T3/T5 学术智能体 ---

type qaFilters struct {
	PublishYear int    `json:"publish_year"`
	Author      string `json:"author"`
	Journal     string `json:"journal"`
}

type qaRequest struct {
	Question string     `json:"question"`
	Filters  *qaFilters `json:"filters"`
}

// matchedBy 依据该 paper 是否出现在 BM25 / 向量列表中判定命中方式。
func matchedBy(paperID string, inBM25, inVector map[string]bool) string {
	kw := inBM25[paperID]
	sem := inVector[paperID]
	switch {
	case kw && sem:
		return "关键词+语义"
	case sem:
		return "语义"
	case kw:
		return "关键词"
	default:
		return "关键词"
	}
}

func idSet(hits []search.Hit) map[string]bool {
	m := make(map[string]bool, len(hits))
	for _, h := range hits {
		m[h.PaperID] = true
	}
	return m
}

func (h *Handlers) QAAnswer(w http.ResponseWriter, r *http.Request) {
	var req qaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	q := strings.TrimSpace(req.Question)
	if q == "" {
		writeError(w, http.StatusBadRequest, "question is empty")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	res, err := h.runSmartRetrieval(ctx, q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	hits := res.Golden

	// 证据选择：默认 Top5；若候选 < 5 或第 3 名后断崖，则取 Top3 且 evidence_sufficient=false。
	evidenceSufficient := true
	n := 5
	if len(hits) < 5 {
		evidenceSufficient = false
		n = 3
	} else if hits[2].Score < hits[1].Score*0.5 {
		evidenceSufficient = false
		n = 3
	}
	if n > len(hits) {
		n = len(hits)
	}
	selected := hits[:n]

	ids := make([]string, 0, len(selected))
	for _, hi := range selected {
		ids = append(ids, hi.PaperID)
	}
	genPapers := h.buildGeneratePapers(ctx, q, ids, 0)

	bmSet := idSet(res.ListBM25)
	vecSet := idSet(res.ListVector)
	scoreByID := make(map[string]float64, len(selected))
	for _, hi := range selected {
		scoreByID[hi.PaperID] = hi.Score
	}
	references := make([]map[string]any, 0, len(genPapers))
	for i, p := range genPapers {
		references = append(references, map[string]any{
			"rank":       i + 1,
			"paper_id":   p.PaperID,
			"title":      p.Title,
			"author":     p.Author,
			"year":       p.PublishYear,
			"doi":        p.DOI,
			"journal":    "",
			"matched_by": matchedBy(p.PaperID, bmSet, vecSet),
			"score":      scoreByID[p.PaperID],
			"snippet":    p.TopChunkText,
		})
	}

	filtersJSON, _ := json.Marshal(req.Filters)
	if err := h.DB.AddHistory("qa", q, string(filtersJSON)); err != nil {
		log.Printf("[history] add: %v", err)
	}

	send, ok := sseStart(w)
	if !ok {
		answer, err := h.Py.QA(ctx, q, genPapers, evidenceSufficient)
		if err != nil {
			writeError(w, http.StatusBadGateway, "qa: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"answer": answer, "evidence_sufficient": evidenceSufficient, "references": references,
		})
		return
	}
	send("meta", map[string]any{"evidence_sufficient": evidenceSufficient, "references": references})
	if err := h.Py.QAStream(ctx, q, genPapers, evidenceSufficient, func(d string) {
		send("delta", map[string]any{"text": d})
	}); err != nil {
		send("error", map[string]any{"message": err.Error()})
	}
	send("done", map[string]any{})
}

type reviewAutoRequest struct {
	Q string `json:"q"`
}

func (h *Handlers) ReviewAuto(w http.ResponseWriter, r *http.Request) {
	var req reviewAutoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	q := strings.TrimSpace(req.Q)
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is empty")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	res, err := h.runSmartRetrieval(ctx, q)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	hits := res.Golden
	if len(hits) > 5 {
		hits = hits[:5]
	}
	ids := make([]string, 0, len(hits))
	for _, hi := range hits {
		ids = append(ids, hi.PaperID)
	}
	genPapers := h.buildGeneratePapers(ctx, q, ids, 0)
	citations := citationsFromGenPapers(genPapers)

	send, ok := sseStart(w)
	if !ok {
		resp, err := h.Py.Generate(ctx, pyclient.GenerateRequest{Query: q, Papers: genPapers})
		if err != nil {
			writeError(w, http.StatusBadGateway, "generate: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"answer": resp.Answer, "citations": citations})
		return
	}
	send("meta", map[string]any{"citations": citations, "matched": nil})
	if err := h.Py.GenerateStream(ctx, pyclient.GenerateRequest{Query: q, Papers: genPapers}, func(d string) {
		send("delta", map[string]any{"text": d})
	}); err != nil {
		send("error", map[string]any{"message": err.Error()})
	}
	send("done", map[string]any{})
}

type reviewManualRequest struct {
	DOI     string `json:"doi"`
	Title   string `json:"title"`
	Text    string `json:"text"`
	Author  string `json:"author"`
	Year    int    `json:"year"`
	Journal string `json:"journal"`
}

func (h *Handlers) ReviewManual(w http.ResponseWriter, r *http.Request) {
	var req reviewManualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	var genPapers []pyclient.GeneratePaper
	var matched map[string]any
	query := strings.TrimSpace(req.Title)

	if t := strings.TrimSpace(req.Text); t != "" {
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = "用户提供文本"
		}
		genPapers = []pyclient.GeneratePaper{{
			Title:              title,
			Author:             strings.TrimSpace(req.Author),
			DOI:                strings.TrimSpace(req.DOI),
			PublishYear:        req.Year,
			ResearchDesignText: t,
			Abstract:           t,
			TopChunkText:       t,
		}}
		if query == "" {
			query = title
		}
		matched = nil
	} else {
		doi := strings.TrimSpace(req.DOI)
		title := strings.TrimSpace(req.Title)
		if doi == "" && title == "" {
			writeError(w, http.StatusBadRequest, "doi/title/text required")
			return
		}
		_, _, papersMap := h.snapshot()
		var found *store.Paper
		for i := range papersMap {
			p := papersMap[i]
			if (doi != "" && p.DOI == doi) || (title != "" && p.Title == title) {
				pp := p
				found = &pp
				break
			}
		}
		if found == nil {
			writeError(w, http.StatusNotFound, "未能精确定位到库内文献")
			return
		}
		if query == "" {
			query = found.Title
		}
		genPapers = h.buildGeneratePapers(ctx, query, []string{found.PaperID}, 0)
		matched = map[string]any{"paper_id": found.PaperID, "title": found.Title}
	}

	citations := citationsFromGenPapers(genPapers)
	send, ok := sseStart(w)
	if !ok {
		resp, err := h.Py.Generate(ctx, pyclient.GenerateRequest{Query: query, Papers: genPapers})
		if err != nil {
			writeError(w, http.StatusBadGateway, "generate: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"answer": resp.Answer, "citations": citations, "matched": matched})
		return
	}
	send("meta", map[string]any{"citations": citations, "matched": matched})
	if err := h.Py.GenerateStream(ctx, pyclient.GenerateRequest{Query: query, Papers: genPapers}, func(d string) {
		send("delta", map[string]any{"text": d})
	}); err != nil {
		send("error", map[string]any{"message": err.Error()})
	}
	send("done", map[string]any{})
}

// paperContext 取论文并组装 PaperContext（含 FullText=RawBody）。论文不存在返回 nil。
func (h *Handlers) paperContext(id string) (*pyclient.PaperContext, error) {
	p, err := h.DB.GetPaper(id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, nil
	}
	return &pyclient.PaperContext{
		PaperID:            p.PaperID,
		Title:              p.Title,
		Author:             p.Author,
		DOI:                p.DOI,
		PublishYear:        p.PublishYear,
		Journal:            p.SourceJournal,
		Keywords:           p.Keywords,
		Abstract:           p.Abstract,
		ResearchDesignText: p.ResearchDesignText,
		FullText:           p.RawBody,
	}, nil
}

type paperChatRequest struct {
	Question string `json:"question"`
}

func (h *Handlers) PaperChat(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req paperChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	pc, err := h.paperContext(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pc == nil {
		writeError(w, http.StatusNotFound, "paper not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	question := strings.TrimSpace(req.Question)
	snippets := pickSnippets(pc.FullText, question, 3)

	send, ok := sseStart(w)
	if !ok {
		res, err := h.Py.Chat(ctx, *pc, req.Question)
		if err != nil {
			writeError(w, http.StatusBadGateway, "chat: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"answer": res.Answer, "evidence_snippets": res.EvidenceSnippets,
		})
		return
	}
	send("meta", map[string]any{"evidence_snippets": snippets})
	if err := h.Py.ChatStream(ctx, *pc, question, func(d string) {
		send("delta", map[string]any{"text": d})
	}); err != nil {
		send("error", map[string]any{"message": err.Error()})
	}
	send("done", map[string]any{})
}

func (h *Handlers) PaperSummary(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pc, err := h.paperContext(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pc == nil {
		writeError(w, http.StatusNotFound, "paper not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := h.Py.Summary(ctx, *pc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "summary: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summary":  res.Summary,
		"method":   res.Method,
		"result":   res.Result,
		"keywords": res.Keywords,
	})
}

func (h *Handlers) PaperMindmap(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pc, err := h.paperContext(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if pc == nil {
		writeError(w, http.StatusNotFound, "paper not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	markdown, err := h.Py.Mindmap(ctx, *pc)
	if err != nil {
		writeError(w, http.StatusBadGateway, "mindmap: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"markdown": markdown})
}

func (h *Handlers) PaperRelated(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	idx, chunks, papers := h.snapshot()
	if idx == nil {
		writeError(w, http.StatusServiceUnavailable, "index not ready")
		return
	}
	seed, ok := papers[id]
	if !ok {
		pp, err := h.DB.GetPaper(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if pp == nil {
			writeError(w, http.StatusNotFound, "paper not found")
			return
		}
		seed = *pp
	}

	// BM25 路：用该论文 keywords 分词全量检索。
	idx.SetAllowed(nil)
	seedTokens := search.Tokenize(seed.Keywords)
	listA := idx.QueryBM25(seedTokens, search.DefaultFieldWeights(), 50)

	// 向量路：取该论文自己最长的 chunk 的 embedding 作为种子向量。
	var listB []search.Hit
	var seedVec []float32
	bestLen := -1
	for _, c := range chunks {
		if c.PaperID != id || len(c.Embedding) == 0 {
			continue
		}
		if len(c.ChunkText) > bestLen {
			bestLen = len(c.ChunkText)
			seedVec = c.Embedding
		}
	}
	if seedVec != nil {
		chunkHits := search.TopKChunksByVector(chunks, seedVec, nil, 0, 200)
		listB = search.AggregateChunksToPapers(chunkHits, 50)
	}

	golden := search.RRF(listA, listB, 60, 0)
	bmSet := idSet(listA)
	vecSet := idSet(listB)

	related := make([]map[string]any, 0, 20)
	for _, hi := range golden {
		if hi.PaperID == id {
			continue
		}
		p, ok := papers[hi.PaperID]
		if !ok {
			continue
		}
		related = append(related, map[string]any{
			"paper_id":   p.PaperID,
			"title":      p.Title,
			"author":     p.Author,
			"year":       p.PublishYear,
			"doi":        p.DOI,
			"journal":    p.SourceJournal,
			"score":      hi.Score,
			"matched_by": matchedBy(hi.PaperID, bmSet, vecSet),
		})
		if len(related) >= 20 {
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"related_papers": related})
}

func (h *Handlers) Reindex(w http.ResponseWriter, r *http.Request) {
	if err := h.Reload(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
}
