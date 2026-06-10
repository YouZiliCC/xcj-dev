package search

import (
	"fmt"
	"sort"
	"strings"

	"xcjdev/backend/internal/store"
)

// 布尔检索：支持 AND / OR / NOT（含 - 前缀）/ 括号 / "精确短语" / 字段限定 field:term。
//
// 语法（参考 Lucene Query Parser 的子集）：
//
//	expr    := orExpr
//	orExpr  := andExpr ( OR andExpr )*
//	andExpr := notExpr ( [AND] notExpr )*        // 相邻项隐式 AND
//	notExpr := (NOT | '-') notExpr | primary
//	primary := '(' expr ')' | [field ':'] ("..." | word)
//
// 文本字段（title/keywords/abstract/design/body）的词项走 BM25 打分；
// 元数据字段（author/journal/affiliation/clc/year）走子串/前缀匹配，得分记 0（仅作集合过滤）；
// 短语 "..." 在字段原文上做子串匹配，得分取其分词后的 BM25 分。
// AND=交集（分数相加）；OR=并集（分数相加）；NOT=从左侧结果中剔除。

// BoolNode 是布尔表达式树节点。
type BoolNode struct {
	Op       string // "and" | "or" | "not" | "term"
	Children []*BoolNode
	// term 专用
	Field  string // ""=默认字段；title/keywords/abstract/design/body/author/journal/affiliation/clc/year
	Text   string
	Phrase bool
}

var boolFieldAlias = map[string]string{
	"title": "title", "题名": "title", "标题": "title",
	"keywords": "keywords", "keyword": "keywords", "关键词": "keywords",
	"abstract": "abstract", "摘要": "abstract",
	"design": "design", "研究设计": "design",
	"body": "body", "全文": "body", "正文": "body",
	"author": "author", "作者": "author",
	"journal": "journal", "刊名": "journal", "期刊": "journal",
	"affiliation": "affiliation", "单位": "affiliation", "机构": "affiliation",
	"clc": "clc", "中图分类号": "clc", "分类号": "clc",
	"year": "year", "年份": "year",
}

// LooksBoolean 判断查询串是否像布尔表达式（含大写 AND/OR/NOT、括号、引号、字段限定）。
func LooksBoolean(q string) bool {
	if strings.ContainsAny(q, "()\"“”") {
		return true
	}
	for _, w := range strings.Fields(q) {
		switch strings.ToUpper(w) {
		case "AND", "OR", "NOT":
			return true
		}
		if i := strings.IndexAny(w, ":："); i > 0 {
			if _, ok := boolFieldAlias[strings.ToLower(w[:i])]; ok {
				return true
			}
		}
		if strings.HasPrefix(w, "-") && len(w) > 1 {
			return true
		}
	}
	return false
}

// --- 词法 ---

type boolToken struct {
	kind string // "lparen" | "rparen" | "and" | "or" | "not" | "word" | "phrase"
	text string
}

func lexBool(q string) []boolToken {
	// 统一全角符号
	q = strings.NewReplacer("（", "(", "）", ")", "“", `"`, "”", `"`, "：", ":", "　", " ").Replace(q)
	var toks []boolToken
	r := []rune(q)
	i := 0
	for i < len(r) {
		c := r[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(':
			toks = append(toks, boolToken{kind: "lparen"})
			i++
		case c == ')':
			toks = append(toks, boolToken{kind: "rparen"})
			i++
		case c == '"':
			j := i + 1
			for j < len(r) && r[j] != '"' {
				j++
			}
			toks = append(toks, boolToken{kind: "phrase", text: string(r[i+1 : j])})
			if j < len(r) {
				j++
			}
			i = j
		case c == '-' && i+1 < len(r) && r[i+1] != ' ' && r[i+1] != '-':
			toks = append(toks, boolToken{kind: "not"})
			i++
		default:
			j := i
			for j < len(r) && !strings.ContainsRune(" \t\n()\"", r[j]) {
				j++
			}
			w := string(r[i:j])
			switch strings.ToUpper(w) {
			case "AND", "&&":
				toks = append(toks, boolToken{kind: "and"})
			case "OR", "||":
				toks = append(toks, boolToken{kind: "or"})
			case "NOT":
				toks = append(toks, boolToken{kind: "not"})
			default:
				toks = append(toks, boolToken{kind: "word", text: w})
			}
			i = j
		}
	}
	return toks
}

// --- 语法 ---

type boolParser struct {
	toks []boolToken
	pos  int
}

// ParseBoolQuery 解析布尔表达式。出错时返回 error（上层可回退普通检索）。
func ParseBoolQuery(q string) (*BoolNode, error) {
	p := &boolParser{toks: lexBool(q)}
	if len(p.toks) == 0 {
		return nil, fmt.Errorf("empty query")
	}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected token at %d", p.pos)
	}
	return node, nil
}

func (p *boolParser) peek() *boolToken {
	if p.pos >= len(p.toks) {
		return nil
	}
	return &p.toks[p.pos]
}

func (p *boolParser) parseOr() (*BoolNode, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	children := []*BoolNode{left}
	for t := p.peek(); t != nil && t.kind == "or"; t = p.peek() {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return left, nil
	}
	return &BoolNode{Op: "or", Children: children}, nil
}

func (p *boolParser) parseAnd() (*BoolNode, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	children := []*BoolNode{left}
	for t := p.peek(); t != nil; t = p.peek() {
		if t.kind == "and" {
			p.pos++
			continue
		}
		if t.kind == "or" || t.kind == "rparen" {
			break
		}
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		children = append(children, right)
	}
	if len(children) == 1 {
		return left, nil
	}
	return &BoolNode{Op: "and", Children: children}, nil
}

func (p *boolParser) parseNot() (*BoolNode, error) {
	if t := p.peek(); t != nil && t.kind == "not" {
		p.pos++
		child, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &BoolNode{Op: "not", Children: []*BoolNode{child}}, nil
	}
	return p.parsePrimary()
}

func (p *boolParser) parsePrimary() (*BoolNode, error) {
	t := p.peek()
	if t == nil {
		return nil, fmt.Errorf("unexpected end of query")
	}
	switch t.kind {
	case "lparen":
		p.pos++
		node, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if nt := p.peek(); nt == nil || nt.kind != "rparen" {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return node, nil
	case "phrase":
		p.pos++
		return &BoolNode{Op: "term", Text: t.text, Phrase: true}, nil
	case "word":
		p.pos++
		word := t.text
		field := ""
		if i := strings.Index(word, ":"); i > 0 {
			if f, ok := boolFieldAlias[strings.ToLower(word[:i])]; ok {
				field = f
				word = word[i+1:]
			}
		}
		// field:"短语"
		if nt := p.peek(); field != "" && word == "" && nt != nil && nt.kind == "phrase" {
			p.pos++
			return &BoolNode{Op: "term", Field: field, Text: nt.text, Phrase: true}, nil
		}
		if strings.HasPrefix(word, `"`) && strings.HasSuffix(word, `"`) && len(word) > 1 {
			return &BoolNode{Op: "term", Field: field, Text: strings.Trim(word, `"`), Phrase: true}, nil
		}
		if word == "" {
			return nil, fmt.Errorf("empty term")
		}
		return &BoolNode{Op: "term", Field: field, Text: word}, nil
	default:
		return nil, fmt.Errorf("unexpected %s", t.kind)
	}
}

// --- 求值 ---

// EvalBool 在索引上对布尔表达式求值。
// defaultMask 为默认文本字段掩码（来自请求的 field 选项）；allowed 为硬过滤白名单（nil=全放行）。
// 返回 paper_id → 得分。
func (idx *Index) EvalBool(node *BoolNode, w FieldWeights, defaultMask [5]bool, allowed map[string]bool) map[string]float64 {
	switch node.Op {
	case "term":
		return idx.evalTerm(node, w, defaultMask, allowed)
	case "and":
		var acc map[string]float64
		var negs []map[string]float64
		for _, ch := range node.Children {
			if ch.Op == "not" {
				negs = append(negs, idx.EvalBool(ch.Children[0], w, defaultMask, allowed))
				continue
			}
			m := idx.EvalBool(ch, w, defaultMask, allowed)
			if acc == nil {
				acc = m
			} else {
				next := make(map[string]float64)
				for id, s := range acc {
					if s2, ok := m[id]; ok {
						next[id] = s + s2
					}
				}
				acc = next
			}
		}
		if acc == nil {
			// 全是 NOT：从全集（经 allowed 过滤）出发
			acc = idx.universe(allowed)
		}
		for _, neg := range negs {
			for id := range neg {
				delete(acc, id)
			}
		}
		return acc
	case "or":
		out := make(map[string]float64)
		for _, ch := range node.Children {
			for id, s := range idx.EvalBool(ch, w, defaultMask, allowed) {
				out[id] += s
			}
		}
		return out
	case "not":
		// 顶层独立 NOT：全集减去命中集
		out := idx.universe(allowed)
		for id := range idx.EvalBool(node.Children[0], w, defaultMask, allowed) {
			delete(out, id)
		}
		return out
	}
	return nil
}

func (idx *Index) universe(allowed map[string]bool) map[string]float64 {
	out := make(map[string]float64, idx.docCount)
	for _, p := range idx.papers {
		if allowed != nil && !allowed[p.PaperID] {
			continue
		}
		out[p.PaperID] = 0
	}
	return out
}

func maskForField(field string, defaultMask [5]bool) ([5]bool, bool) {
	switch field {
	case "":
		return defaultMask, true
	case "title":
		return [5]bool{true, false, false, false, false}, true
	case "keywords":
		return [5]bool{false, true, false, false, false}, true
	case "abstract":
		return [5]bool{false, false, true, false, false}, true
	case "design":
		return [5]bool{false, false, false, true, false}, true
	case "body":
		return [5]bool{false, false, false, false, true}, true
	}
	return [5]bool{}, false
}

func (idx *Index) evalTerm(node *BoolNode, w FieldWeights, defaultMask [5]bool, allowed map[string]bool) map[string]float64 {
	text := strings.TrimSpace(node.Text)
	if text == "" {
		return map[string]float64{}
	}
	mask, isText := maskForField(node.Field, defaultMask)
	if !isText {
		// 元数据字段：子串匹配
		out := make(map[string]float64)
		lower := strings.ToLower(text)
		for _, p := range idx.papers {
			if allowed != nil && !allowed[p.PaperID] {
				continue
			}
			var hay string
			switch node.Field {
			case "author":
				hay = p.Author
			case "journal":
				hay = p.SourceJournal
			case "affiliation":
				hay = p.Affiliation
			case "clc":
				hay = p.CLCNumber
			case "year":
				hay = fmt.Sprintf("%d", p.PublishYear)
			}
			if hay != "" && strings.Contains(strings.ToLower(hay), lower) {
				out[p.PaperID] = 0
			}
		}
		return out
	}

	// 布尔语义下词项的「命中」必须是字段原文的真子串包含——
	// 中文 unigram/bigram 分词会让 BM25 软命中（"深度学习"命中仅含"学习"的文档），
	// 这对排序无害，但会让 AND 失去约束、NOT 误伤。因此：
	// 成员判定 = 子串包含（含短语）；得分 = BM25（仅用于排序）。
	tokens := Tokenize(text)
	bm := idx.QueryBM25Fields(tokens, w, mask, allowed, 0)
	scores := make(map[string]float64, len(bm))
	for _, h := range bm {
		scores[h.PaperID] = h.Score
	}
	out := make(map[string]float64)
	for _, p := range idx.papers {
		if allowed != nil && !allowed[p.PaperID] {
			continue
		}
		if paperFieldContains(p, mask, text) {
			if s, ok := scores[p.PaperID]; ok && s > 0 {
				out[p.PaperID] = s
			} else {
				out[p.PaperID] = 0.1 // 子串命中但 BM25 无分（极端情况），给保底分
			}
		}
	}
	return out
}

func paperFieldContains(p store.Paper, mask [5]bool, sub string) bool {
	sub = strings.ToLower(sub)
	fields := [5]string{p.Title, p.Keywords, p.Abstract, p.ResearchDesignText, p.RawBody}
	for f := 0; f < 5; f++ {
		if mask[f] && fields[f] != "" && strings.Contains(strings.ToLower(fields[f]), sub) {
			return true
		}
	}
	return false
}

// BoolSearch 解析并求值布尔表达式，返回按得分降序的 Hit 列表。
func (idx *Index) BoolSearch(q string, w FieldWeights, defaultMask [5]bool, allowed map[string]bool, topK int) ([]Hit, error) {
	node, err := ParseBoolQuery(q)
	if err != nil {
		return nil, err
	}
	scores := idx.EvalBool(node, w, defaultMask, allowed)
	hits := make([]Hit, 0, len(scores))
	for id, s := range scores {
		hits = append(hits, Hit{PaperID: id, Score: s, MatchedFields: []string{"boolean"}})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].PaperID < hits[j].PaperID
	})
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	for i := range hits {
		hits[i].Rank = i + 1
	}
	return hits, nil
}
