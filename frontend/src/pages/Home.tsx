import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { StatsResponse } from "../api/types";
import SectionTitle from "../components/SectionTitle";

interface Entry {
  to: string;
  tag: string;
  title: string;
  en: string;
  desc: string;
}

const ENTRIES: Entry[] = [
  {
    to: "/search",
    tag: "01",
    title: "文献检索",
    en: "RETRIEVAL",
    desc: "字段加权 BM25 + 向量语义双路检索。支持 AND/OR/NOT 布尔检索式、高级检索与知网式结果筛选。",
  },
  {
    to: "/qa",
    tag: "02",
    title: "智能问答",
    en: "RAG-QA",
    desc: "提出研究问题，按「概念解释 · 背景 · 方法 · 证据 · 空白」五段结构作答，引用可定位到具体文本块。",
  },
  {
    to: "/review",
    tag: "03",
    title: "文献综述",
    en: "SYNTHESIS",
    desc: "自动综述（检索后自选文献）或自选文献综述（DOI/标题精确定位），生成结构化综述。",
  },
];

export default function Home() {
  const [stats, setStats] = useState<StatsResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api
      .stats()
      .then(setStats)
      .catch((e: Error) => setErr(e.message));
  }, []);

  const years = stats?.year_dist ?? {};
  const yearEntries = Object.entries(years)
    .map(([y, c]) => [Number(y), c] as [number, number])
    .filter(([y]) => y > 0)
    .sort((a, b) => a[0] - b[0]);
  const maxCount = yearEntries.reduce((m, [, c]) => Math.max(m, c), 0) || 1;

  return (
    <div className="stagger space-y-10">
      {/* hero */}
      <section>
        <div className="kicker mb-3">// 信息存储与检索 · 学术智能体平台</div>
        <h1 className="font-display font-extrabold text-3xl md:text-4xl leading-tight">
          检索、问答、综述
          <span className="text-cyan">.</span>
        </h1>
        <p className="mt-3 text-text-2 max-w-2xl leading-relaxed">
          一个面向中文人文社科文献的研究终端。全文按章节与语义边界切分并标注段落作用，
          底层以 BM25 字段加权与稠密向量双路检索 + RRF 融合排序，支持布尔检索式与知网式筛选；
          上层提供五段式智能问答与文献综述，并在每篇论文详情页内置学术智能体。
        </p>
        <div className="mt-5 flex flex-wrap gap-2">
          <span className="chip chip-cyan">SQLite</span>
          <span className="chip chip-cyan">本地 BGE 向量</span>
          <span className="chip chip-cyan">语义边界切分</span>
          <span className="chip chip-amber">BM25 字段加权</span>
          <span className="chip chip-amber">RRF 融合</span>
          <span className="chip chip-amber">布尔检索</span>
          <span className="chip chip-violet">Claude Opus RAG</span>
        </div>
      </section>

      {/* entries */}
      <section>
        <SectionTitle>three entrances · 三大入口</SectionTitle>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          {ENTRIES.map((e) => (
            <Link
              key={e.to}
              to={e.to}
              className="term-panel term-panel-hover p-5 group flex flex-col"
            >
              <div className="flex items-baseline justify-between">
                <span className="mono text-cyan text-2xl font-semibold tnum">
                  {e.tag}
                </span>
                <span className="kicker">{e.en}</span>
              </div>
              <h3 className="mt-4 font-display font-semibold text-lg group-hover:text-cyan transition-colors">
                {e.title}
              </h3>
              <p className="mt-2 text-[13px] text-text-2 leading-relaxed flex-1">
                {e.desc}
              </p>
              <span className="mt-4 kicker text-cyan opacity-0 group-hover:opacity-100 transition-opacity">
                ▸ enter
              </span>
            </Link>
          ))}
        </div>
      </section>

      {/* stats */}
      <section>
        <SectionTitle
          right={
            err ? <span className="mono text-xs text-red">offline</span> : null
          }
        >
          corpus stats · 馆藏概览
        </SectionTitle>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
          <Stat label="papers · 论文" value={stats?.paper_count} />
          <Stat label="chunks · 切块" value={stats?.chunk_count} />
          <Stat
            label="years · 年份跨度"
            value={
              yearEntries.length
                ? `${yearEntries[0][0]}–${yearEntries[yearEntries.length - 1][0]}`
                : undefined
            }
          />
          <Stat label="journals · 刊物" value={stats?.top_journals?.length} />
        </div>

        {/* year distribution bars */}
        {yearEntries.length > 0 && (
          <div className="term-panel mt-4 p-5">
            <div className="kicker mb-4">year distribution · 年份分布</div>
            <div className="flex items-end gap-3 h-40">
              {yearEntries.map(([y, c]) => (
                <div key={y} className="flex-1 flex flex-col items-center gap-2">
                  <span className="mono text-[11px] text-amber tnum">{c}</span>
                  <div className="w-full flex items-end justify-center h-28">
                    <div
                      className="w-full max-w-[44px] border border-cyan"
                      style={{
                        height: `${(c / maxCount) * 100}%`,
                        background: "rgba(var(--accent-rgb), 0.12)",
                      }}
                    />
                  </div>
                  <span className="mono text-[11px] text-text-3 tnum">{y}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

function Stat({ label, value }: { label: string; value?: number | string }) {
  return (
    <div className="term-panel p-4">
      <div className="kicker">{label}</div>
      <div className="mt-2 font-display font-bold text-2xl tnum text-text">
        {value ?? <span className="text-text-3">—</span>}
      </div>
    </div>
  );
}
