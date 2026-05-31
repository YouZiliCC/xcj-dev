package search

import (
	"math"
	"sort"

	"xcjdev/backend/internal/store"
)

// Cosine 计算两个向量的余弦相似度。向量为 0 或长度不一致时返回 0。
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		da := float64(a[i])
		db := float64(b[i])
		dot += da * db
		na += da * da
		nb += db * db
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ChunkHit 是 chunk 级别的命中结果。
type ChunkHit struct {
	ChunkID    string  `json:"chunk_id"`
	PaperID    string  `json:"paper_id"`
	Score      float64 `json:"score"`
	Rank       int     `json:"rank"`
	ChunkIndex int     `json:"chunk_index"`
	ChunkText  string  `json:"chunk_text"`
}

// TopKChunksByVector 按余弦相似度对 chunks 排序，过滤不在 allowed 中的 paper（allowed 为 nil 时全放行），返回前 topK。
// minScore 为相似度下限：cosine < minScore 的 chunk 直接丢弃（用于精度：避免大量弱相关 chunk 进入向量榜）。minScore<=0 表示不设下限。
func TopKChunksByVector(chunks []store.Chunk, query []float32, allowed map[string]bool, minScore float64, topK int) []ChunkHit {
	if len(chunks) == 0 || len(query) == 0 {
		return nil
	}
	hits := make([]ChunkHit, 0, len(chunks))
	for _, c := range chunks {
		if allowed != nil && !allowed[c.PaperID] {
			continue
		}
		if len(c.Embedding) == 0 {
			continue
		}
		s := Cosine(c.Embedding, query)
		if s <= 0 || s < minScore {
			continue
		}
		hits = append(hits, ChunkHit{
			ChunkID:    c.ChunkID,
			PaperID:    c.PaperID,
			Score:      s,
			ChunkIndex: c.ChunkIndex,
			ChunkText:  c.ChunkText,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	for i := range hits {
		hits[i].Rank = i + 1
	}
	return hits
}

// AggregateChunksToPapers 按 paper_id 聚合 chunk 级得分（取最高分），返回按分数降序的 paper Hit 列表。
func AggregateChunksToPapers(hits []ChunkHit, topK int) []Hit {
	if len(hits) == 0 {
		return nil
	}
	best := make(map[string]float64, len(hits))
	for _, h := range hits {
		if cur, ok := best[h.PaperID]; !ok || h.Score > cur {
			best[h.PaperID] = h.Score
		}
	}
	out := make([]Hit, 0, len(best))
	for pid, s := range best {
		out = append(out, Hit{
			PaperID:       pid,
			Score:         s,
			MatchedFields: []string{"chunk"},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if topK > 0 && len(out) > topK {
		out = out[:topK]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
