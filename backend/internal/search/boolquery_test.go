package search

import (
	"testing"

	"xcjdev/backend/internal/store"
)

func testIndex() *Index {
	papers := []store.Paper{
		{PaperID: "p1", Title: "深度学习图像分类研究", TitleTokens: TokenizeJoin("深度学习图像分类研究"), Author: "张三;李四", SourceJournal: "计算机学报", PublishYear: 2024, Abstract: "本文研究深度学习", AbstractTokens: TokenizeJoin("本文研究深度学习")},
		{PaperID: "p2", Title: "机器学习目标检测综述", TitleTokens: TokenizeJoin("机器学习目标检测综述"), Author: "王五", SourceJournal: "软件学报", PublishYear: 2023, Abstract: "目标检测综述", AbstractTokens: TokenizeJoin("目标检测综述")},
		{PaperID: "p3", Title: "图像分类的传统方法", TitleTokens: TokenizeJoin("图像分类的传统方法"), Author: "张三", SourceJournal: "计算机学报", PublishYear: 2022, Abstract: "不涉及学习", AbstractTokens: TokenizeJoin("不涉及学习")},
	}
	return Build(papers)
}

func TestLooksBoolean(t *testing.T) {
	for _, q := range []string{"A AND B", "(深度学习 OR 机器学习)", `"对比学习"`, "深度学习 NOT 综述", "title:深度学习", "-综述 图像"} {
		if !LooksBoolean(q) {
			t.Errorf("LooksBoolean(%q) = false, want true", q)
		}
	}
	for _, q := range []string{"深度学习图像分类", "android开发", "信息安全 风险管理"} {
		if LooksBoolean(q) {
			t.Errorf("LooksBoolean(%q) = true, want false", q)
		}
	}
}

func TestBoolSearch(t *testing.T) {
	idx := testIndex()
	w := DefaultFieldWeights()

	cases := []struct {
		q    string
		want map[string]bool
	}{
		{"深度学习 AND 图像分类", map[string]bool{"p1": true}},
		{"(深度学习 OR 机器学习) AND NOT 综述", map[string]bool{"p1": true}},
		{"图像分类 NOT 深度学习", map[string]bool{"p3": true}},
		{"author:张三 AND 图像分类", map[string]bool{"p1": true, "p3": true}},
		{"journal:计算机学报 NOT 深度", map[string]bool{"p3": true}},
		{`"目标检测"`, map[string]bool{"p2": true}},
		{"year:2024 OR year:2023", map[string]bool{"p1": true, "p2": true}},
	}
	for _, c := range cases {
		hits, err := idx.BoolSearch(c.q, w, AllFields, nil, 0)
		if err != nil {
			t.Errorf("BoolSearch(%q) error: %v", c.q, err)
			continue
		}
		got := map[string]bool{}
		for _, h := range hits {
			got[h.PaperID] = true
		}
		if len(got) != len(c.want) {
			t.Errorf("BoolSearch(%q) = %v, want %v", c.q, got, c.want)
			continue
		}
		for id := range c.want {
			if !got[id] {
				t.Errorf("BoolSearch(%q) missing %s; got %v", c.q, id, got)
			}
		}
	}
}

func TestBoolParseErrors(t *testing.T) {
	for _, q := range []string{"(深度学习", "AND", ""} {
		if _, err := ParseBoolQuery(q); err == nil {
			t.Errorf("ParseBoolQuery(%q) expected error", q)
		}
	}
}
