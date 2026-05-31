package search

import "sort"

func hasField(fields []string, name string) bool {
	for _, f := range fields {
		if f == name {
			return true
		}
	}
	return false
}

// PrecisionFilter 对 RRF 融合后的榜单做精度裁剪（仅用于智能检索 golden，提高查准率）：
//  1. 尾部去 BM25-only：排名超过 keepHead 的条目，若不在向量臂中（MatchedFields 无 "chunk"），丢弃。
//     —— RRF 在向量臂耗尽后会持续追加只靠字符重叠命中的 BM25 结果，正是无关文献的主要来源。
//  2. 断崖截断：从第 keepHead… 起，遇到相邻分数骤降（next < gapRatio*cur）即在此截断（保护前 minKeep 条不截）。
//  3. 上限 maxOut。
func PrecisionFilter(hits []Hit, keepHead int, gapRatio float64, maxOut int) []Hit {
	if len(hits) == 0 {
		return hits
	}
	filtered := make([]Hit, 0, len(hits))
	for i, h := range hits {
		rank := i + 1
		if rank <= keepHead || hasField(h.MatchedFields, "chunk") {
			filtered = append(filtered, h)
		}
	}
	// 断崖截断：i>=minKeep-1 起找首个骤降点（minKeep=4，保护窄查询只有 4-5 条的召回）。
	const minKeep = 4
	if gapRatio > 0 {
		for i := minKeep - 1; i+1 < len(filtered); i++ {
			if filtered[i+1].Score < gapRatio*filtered[i].Score {
				filtered = filtered[:i+1]
				break
			}
		}
	}
	if maxOut > 0 && len(filtered) > maxOut {
		filtered = filtered[:maxOut]
	}
	for i := range filtered {
		filtered[i].Rank = i + 1
	}
	return filtered
}

// RRF 倒数排名融合。k 默认 60；topK<=0 表示不截断。
func RRF(listA, listB []Hit, k int, topK int) []Hit {
	if k <= 0 {
		k = 60
	}
	type agg struct {
		score   float64
		fields  map[string]struct{}
		paperID string
	}
	m := make(map[string]*agg)
	add := func(list []Hit) {
		for _, h := range list {
			rank := h.Rank
			if rank <= 0 {
				continue
			}
			a, ok := m[h.PaperID]
			if !ok {
				a = &agg{fields: map[string]struct{}{}, paperID: h.PaperID}
				m[h.PaperID] = a
			}
			a.score += 1.0 / float64(k+rank)
			for _, f := range h.MatchedFields {
				a.fields[f] = struct{}{}
			}
		}
	}
	add(listA)
	add(listB)
	out := make([]Hit, 0, len(m))
	for _, a := range m {
		fields := make([]string, 0, len(a.fields))
		for f := range a.fields {
			fields = append(fields, f)
		}
		sort.Strings(fields)
		out = append(out, Hit{
			PaperID:       a.paperID,
			Score:         a.score,
			MatchedFields: fields,
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
