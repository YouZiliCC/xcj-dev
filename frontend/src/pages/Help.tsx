import SectionTitle from "../components/SectionTitle";

export default function Help() {
  return (
    <div className="space-y-8 stagger max-w-3xl">
      <div>
        <div className="kicker mb-3">// colophon</div>
        <h1 className="font-display font-bold text-2xl">帮助与说明</h1>
      </div>

      <section>
        <SectionTitle>retrieval · 检索原理</SectionTitle>
        <div className="space-y-3 text-sm leading-relaxed text-text-2">
          <p>
            系统对每篇论文按字段（标题/关键词/摘要/研究设计/正文）分别建立倒排，检索时用
            <span className="text-amber"> BM25 </span>
            打分并按字段加权（标题 8 · 关键词 5 · 研究设计 4 · 摘要 3 · 正文 1）。
          </p>
          <p>
            传统检索支持
            <span className="text-amber"> 布尔检索式 </span>
            （AND / OR / NOT / 括号 / "精确短语" / title: 等字段限定），并提供高级检索面板与知网式结果筛选：
            作者、作者单位、刊名、核心期刊、中图分类号、年份区间，同字段多值为「或」、字段之间为「与」，
            筛选在检索前生效（Allowed IDs 预过滤）。
          </p>
          <p>
            全文采用<span className="text-cyan">两阶段语义切分</span>：先按章节标题分章并为整章标注段落作用
            （概念解释 / 背景说明 / 方法依据 / 经验证据 / 研究空白），再在章内按句间语义边界聚合为约 500
            字的文本块，保证不切断句子、主题集中。
          </p>
          <p>
            智能检索额外引入
            <span className="text-violet"> 稠密向量 </span>
            语义召回：用本地 BGE 模型把查询与文本块编码为向量做余弦近邻，再与 BM25 结果用
            <span className="text-cyan"> RRF </span>
            （倒数排名融合，k=60）合成黄金排行榜；查询改写同时给出可复用的布尔检索式。
          </p>
        </div>
      </section>

      <section>
        <SectionTitle>agents · 智能体能力</SectionTitle>
        <ul className="space-y-2 text-sm leading-relaxed text-text-2">
          <li><span className="chip chip-cyan mr-2">智能问答</span>按「概念解释 · 背景说明 · 方法依据 · 经验证据 · 研究空白」五段固定结构作答，段内 [n] 引用可点击定位到具体文本块，附参考文献表。</li>
          <li><span className="chip chip-cyan mr-2">文献综述</span>自动综述（检索自选）/ 自选综述（DOI/标题精确定位）。</li>
          <li><span className="chip chip-cyan mr-2">AI 概要</span>自动产出论文概要 / 方法 / 结果 / 关键词。</li>
          <li><span className="chip chip-cyan mr-2">AI 同读</span>基于单篇论文全文问答。</li>
          <li><span className="chip chip-cyan mr-2">思维导图</span>由论文生成 Markdown 大纲并以 markmap 渲染为可展开导图。</li>
          <li><span className="chip chip-cyan mr-2">相关文献</span>以本文关键词 + 摘要向量复刻双路检索，加权给出相关论文。</li>
        </ul>
      </section>

      <section>
        <SectionTitle>data · 数据来源</SectionTitle>
        <p className="text-sm leading-relaxed text-text-2">
          语料来自万方学术中文人文社科论文（Word 全文 + CSV 元数据，含期刊文献与学位论文），仅用于课程教学。
          底层存储为 SQLite（关系数据 + 内嵌向量），LLM 使用 Claude（Opus 4.8，OpenAI 兼容接入），
          嵌入使用本地 BAAI/bge-small-zh-v1.5。
        </p>
      </section>
    </div>
  );
}
