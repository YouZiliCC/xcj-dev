import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, HttpError } from "../api/client";
import type {
  MindmapResponse,
  Paper,
  RelatedResponse,
  SummaryResponse,
} from "../api/types";
import SectionTitle from "../components/SectionTitle";
import Loading from "../components/Loading";
import Markdown from "../components/Markdown";
import Mindmap from "../components/Mindmap";
import MatchChips from "../components/MatchChips";
import ScoreBar from "../components/ScoreBar";
import { citationGB, joinAuthors, splitKeywords } from "../lib/format";

const FULLTEXT_PREVIEW = 900;

interface SectionRef {
  id: string;
  label: string;
}

export default function PaperDetail() {
  const { id } = useParams<{ id: string }>();
  const [paper, setPaper] = useState<Paper | null>(null);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [showChunks, setShowChunks] = useState(false);
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!id) return;
    setLoading(true);
    setErr(null);
    setExpanded(false);
    api
      .paper(id)
      .then(setPaper)
      .catch((e: Error) => setErr(e.message))
      .finally(() => setLoading(false));
  }, [id]);

  if (loading) return <Loading label="LOADING PAPER" />;
  if (err)
    return (
      <div className="space-y-4">
        <div className="term-panel px-4 py-3 mono text-sm text-red" style={{ borderColor: "var(--red)" }}>
          ERROR · {err}
        </div>
        <Link to="/search" className="kicker text-cyan">
          ← 返回检索
        </Link>
      </div>
    );
  if (!paper) return null;

  const kws = splitKeywords(paper.keywords);
  const full = paper.full_text || "";
  const isLong = full.length > FULLTEXT_PREVIEW;
  const shownText = expanded || !isLong ? full : full.slice(0, FULLTEXT_PREVIEW);
  const citation = citationGB({
    author: paper.author,
    title: paper.title,
    journal: paper.source_journal,
    year: paper.publish_year,
    doi: paper.doi,
  });

  const sections: SectionRef[] = [
    paper.abstract && { id: "sec-abstract", label: "摘要" },
    paper.research_design_text && { id: "sec-design", label: "研究设计" },
    { id: "sec-fulltext", label: "全文" },
    paper.chunks?.length ? { id: "sec-chunks", label: "切分段" } : null,
    { id: "sec-citation", label: "引用" },
  ].filter(Boolean) as SectionRef[];

  function copy() {
    navigator.clipboard?.writeText(citation).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <div className="space-y-8">
      <Link to="/search" className="kicker text-text-3 hover:text-cyan transition-colors">
        ← back to search
      </Link>

      {/* header */}
      <header>
        <h1 className="font-display font-bold text-2xl md:text-3xl leading-tight">{paper.title}</h1>
        <div className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 mono text-xs text-text-2">
          {paper.doi && <span className="text-text-3">{paper.doi}</span>}
          {paper.doi && <span className="text-text-3">//</span>}
          <span className="text-amber tnum">{paper.publish_year || "—"}</span>
          <span className="text-text-3">//</span>
          <span>{paper.source_journal || "—"}</span>
        </div>
        <div className="mt-2 text-sm text-text-2">{joinAuthors(paper.author) || "—"}</div>
        {paper.affiliation && (
          <div className="mt-1 text-xs text-text-3">{paper.affiliation}</div>
        )}
        {kws.length > 0 && (
          <div className="mt-3 flex flex-wrap gap-1.5">
            {kws.map((k) => (
              <span key={k} className="chip">
                {k}
              </span>
            ))}
          </div>
        )}
      </header>

      <div className="grid grid-cols-12 gap-6 lg:gap-8">
        {/* left TOC */}
        <aside className="hidden lg:block lg:col-span-2">
          <SectionNav sections={sections} />
        </aside>

        {/* main column */}
        <div className="col-span-12 lg:col-span-6 space-y-8">
          {paper.abstract && (
            <section id="sec-abstract" className="scroll-mt-6">
              <SectionTitle>abstract · 摘要</SectionTitle>
              <p className="text-[14px] leading-relaxed text-text-2 whitespace-pre-wrap">{paper.abstract}</p>
            </section>
          )}

          {paper.research_design_text && (
            <section id="sec-design" className="scroll-mt-6">
              <SectionTitle>research design · 研究设计</SectionTitle>
              <p className="text-[14px] leading-relaxed text-text-2 whitespace-pre-wrap">
                {paper.research_design_text}
              </p>
            </section>
          )}

          {/* T1: continuous full text */}
          <section id="sec-fulltext" className="scroll-mt-6">
            <SectionTitle>full text · 全文原文</SectionTitle>
            {full ? (
              <div className="term-panel p-5">
                <p className="text-[14px] leading-[1.85] text-text whitespace-pre-wrap">{shownText}</p>
                {isLong && (
                  <button
                    onClick={() => setExpanded((v) => !v)}
                    className="mt-3 kicker text-cyan hover:text-text transition-colors"
                  >
                    {expanded ? "▾ 收起全文" : "▸ 展开全文"}
                  </button>
                )}
              </div>
            ) : (
              <p className="text-text-3 text-sm italic">（暂无全文）</p>
            )}
          </section>

          {/* chunks moved to secondary collapsed area */}
          {paper.chunks?.length > 0 && (
            <section id="sec-chunks" className="scroll-mt-6">
              <button
                onClick={() => setShowChunks((v) => !v)}
                className="section-title hover:text-cyan transition-colors"
              >
                {showChunks ? "▾" : "▸"} retrieval evidence · 切分段（{paper.chunks.length}）
              </button>
              {showChunks && (
                <div className="mt-3 space-y-2 fade-in">
                  {paper.chunks.map((c, i) => (
                    <div key={c.chunk_id ?? i} className="term-panel px-4 py-3">
                      <div className="kicker mb-1.5 flex flex-wrap items-center gap-1.5">
                        <span>§{(c.chunk_index ?? i) + 1}</span>
                        {c.chapter_title && <span className="chip">{c.chapter_title}</span>}
                        {c.section_role && c.section_role !== "其他" && (
                          <span className="chip chip-violet">{c.section_role}</span>
                        )}
                      </div>
                      <p className="text-[13px] leading-relaxed text-text-2 whitespace-pre-wrap">
                        {c.chunk_text}
                      </p>
                    </div>
                  ))}
                </div>
              )}
            </section>
          )}

          {/* citation */}
          <section id="sec-citation" className="border-t border-line pt-5 scroll-mt-6">
            <SectionTitle
              right={
                <button onClick={copy} className="btn-term">
                  {copied ? "copied ✓" : "copy"}
                </button>
              }
            >
              citation · GB/T 7714
            </SectionTitle>
            <p className="mono text-xs text-text-2 break-words">{citation}</p>
          </section>
        </div>

        {/* agent panel */}
        <div className="col-span-12 lg:col-span-4">
          <div className="lg:sticky lg:top-6">
            <AgentPanel paper={paper} />
          </div>
        </div>
      </div>
    </div>
  );
}

/* ----------------------- section nav (左侧目录) ----------------------- */

function SectionNav({ sections }: { sections: SectionRef[] }) {
  const [active, setActive] = useState(sections[0]?.id ?? "");

  useEffect(() => {
    const obs = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top);
        if (visible[0]) setActive(visible[0].target.id);
      },
      { rootMargin: "-15% 0px -70% 0px", threshold: 0 },
    );
    sections.forEach((s) => {
      const el = document.getElementById(s.id);
      if (el) obs.observe(el);
    });
    return () => obs.disconnect();
  }, [sections]);

  return (
    <nav className="lg:sticky lg:top-6 space-y-1">
      <div className="kicker mb-2">目录 · contents</div>
      {sections.map((s) => (
        <a
          key={s.id}
          href={`#${s.id}`}
          onClick={(e) => {
            e.preventDefault();
            document.getElementById(s.id)?.scrollIntoView({ behavior: "smooth", block: "start" });
          }}
          className={`block text-sm py-1 px-2 border-l-2 transition-colors ${
            active === s.id ? "border-cyan text-cyan" : "border-line text-text-3 hover:text-text-2"
          }`}
        >
          {s.label}
        </a>
      ))}
    </nav>
  );
}

/* ----------------------- agent panel ----------------------- */

type AgentTab = "chat" | "summary" | "mindmap" | "related";

function AgentPanel({ paper }: { paper: Paper }) {
  const [tab, setTab] = useState<AgentTab>("summary");
  return (
    <div className="term-panel p-4">
      <div className="kicker mb-3">▸ academic agent · 学术智能体</div>
      <div className="flex flex-wrap gap-1 border-b border-line mb-4">
        {([
          ["summary", "AI 概要"],
          ["chat", "AI 同读"],
          ["mindmap", "思维导图"],
          ["related", "相关文献"],
        ] as const).map(([k, lbl]) => (
          <button key={k} className={`tab-term ${tab === k ? "tab-active" : ""}`} onClick={() => setTab(k)}>
            {lbl}
          </button>
        ))}
      </div>
      {tab === "summary" && <SummaryTab id={paper.paper_id} />}
      {tab === "chat" && <ChatTab id={paper.paper_id} />}
      {tab === "mindmap" && <MindmapTab id={paper.paper_id} />}
      {tab === "related" && <RelatedTab id={paper.paper_id} />}
    </div>
  );
}

function useAgent<T>(fn: () => Promise<T>) {
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [data, setData] = useState<T | null>(null);
  const go = async () => {
    setLoading(true);
    setErr(null);
    try {
      setData(await fn());
    } catch (e) {
      setErr(e instanceof HttpError ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };
  return { loading, err, data, go, setData };
}

function AgentError({ msg }: { msg: string }) {
  return <div className="mono text-xs text-red mt-2">ERROR · {msg}</div>;
}

function SummaryTab({ id }: { id: string }) {
  const { loading, err, data, go } = useAgent<SummaryResponse>(() => api.paperSummary(id));
  return (
    <div>
      {!data && !loading && (
        <button className="btn-term btn-primary w-full justify-center" onClick={go}>
          ▸ 生成概要
        </button>
      )}
      {loading && <Loading label="SUMMARIZING" />}
      {err && <AgentError msg={err} />}
      {data && (
        <div className="space-y-3 fade-in">
          <Block label="概要" text={data.summary} />
          <Block label="方法" text={data.method} />
          <Block label="结果" text={data.result} />
          {data.keywords?.length > 0 && (
            <div>
              <div className="kicker mb-1.5">关键词</div>
              <div className="flex flex-wrap gap-1.5">
                {data.keywords.map((k) => (
                  <span key={k} className="chip chip-cyan">
                    {k}
                  </span>
                ))}
              </div>
            </div>
          )}
          <button className="btn-term w-full justify-center" onClick={go}>
            ↻ 重新生成
          </button>
        </div>
      )}
    </div>
  );
}

function Block({ label, text }: { label: string; text: string }) {
  if (!text) return null;
  return (
    <div>
      <div className="kicker mb-1">{label}</div>
      <Markdown source={text} className="text-[13px]" />
    </div>
  );
}

function ChatTab({ id }: { id: string }) {
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [snippets, setSnippets] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [started, setStarted] = useState(false);

  async function go() {
    if (!question.trim()) return;
    setAnswer("");
    setSnippets([]);
    setErr(null);
    setLoading(true);
    setStreaming(true);
    setStarted(true);
    try {
      await api.paperChatStream(
        id,
        { question: question.trim() },
        {
          onMeta: (m) => setSnippets(m.evidence_snippets ?? []),
          onDelta: (t) => {
            setLoading(false);
            setAnswer((a) => a + t);
          },
          onError: (msg) => setErr(msg),
        },
      );
    } catch (e) {
      setErr(e instanceof HttpError ? e.message : String(e));
    } finally {
      setLoading(false);
      setStreaming(false);
    }
  }

  return (
    <div className="space-y-3">
      <textarea
        value={question}
        onChange={(e) => setQuestion(e.target.value)}
        rows={3}
        placeholder="就这篇论文提问，例如：本文的核心论点是什么？"
        className="input-term resize-y font-sans"
      />
      <button className="btn-term btn-primary w-full justify-center" disabled={streaming || !question.trim()} onClick={go}>
        {streaming ? "thinking…" : "▸ 提问"}
      </button>
      {loading && <Loading label="READING" />}
      {err && <AgentError msg={err} />}
      {started && (answer || (streaming && !loading)) && (
        <div className="fade-in space-y-2">
          <div>
            <Markdown source={answer} className="text-[13px]" />
            {streaming && <span className="animate-pulse text-cyan">▍</span>}
          </div>
          {snippets.length > 0 && (
            <details className="mt-2">
              <summary className="kicker text-cyan cursor-pointer">▸ 原文依据</summary>
              <div className="mt-2 space-y-2">
                {snippets.map((s, i) => (
                  <p key={i} className="text-[12px] leading-relaxed text-text-2 border-l-2 border-line-2 pl-3 whitespace-pre-wrap">
                    {s}
                  </p>
                ))}
              </div>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

function MindmapTab({ id }: { id: string }) {
  const { loading, err, data, go } = useAgent<MindmapResponse>(() => api.paperMindmap(id));
  return (
    <div>
      {!data && !loading && (
        <button className="btn-term btn-primary w-full justify-center" onClick={go}>
          ▸ 生成思维导图
        </button>
      )}
      {loading && <Loading label="MAPPING" />}
      {err && <AgentError msg={err} />}
      {data && (
        <div className="fade-in space-y-3">
          <div className="border border-line rounded-sm bg-bg overflow-hidden">
            <Mindmap markdown={data.markdown} />
          </div>
          <button className="btn-term w-full justify-center" onClick={go}>
            ↻ 重新生成
          </button>
        </div>
      )}
    </div>
  );
}

function RelatedTab({ id }: { id: string }) {
  const { loading, err, data, go } = useAgent<RelatedResponse>(() => api.paperRelated(id));
  const max = (data?.related_papers ?? []).reduce((m, r) => Math.max(m, r.score), 0.0001);
  return (
    <div>
      {!data && !loading && (
        <button className="btn-term btn-primary w-full justify-center" onClick={go}>
          ▸ 检索相关文献
        </button>
      )}
      {loading && <Loading label="RETRIEVING" />}
      {err && <AgentError msg={err} />}
      {data && (
        <div className="space-y-2 fade-in">
          {data.related_papers.length === 0 ? (
            <p className="text-text-3 text-sm italic">— 无相关文献 —</p>
          ) : (
            data.related_papers.map((r, i) => (
              <Link
                key={r.paper_id + i}
                to={`/paper/${encodeURIComponent(r.paper_id)}`}
                className="term-panel term-panel-hover block px-3 py-2.5 group"
              >
                <div className="flex items-start gap-2">
                  <span className="mono text-text-3 text-xs tnum pt-0.5">{String(i + 1).padStart(2, "0")}</span>
                  <div className="flex-1 min-w-0">
                    <div className="text-[13px] text-text group-hover:text-cyan transition-colors leading-snug">
                      {r.title}
                    </div>
                    <div className="mt-1 flex items-center gap-2 text-[11px] text-text-2">
                      <span className="mono tnum text-amber">{r.year || "—"}</span>
                      <MatchChips matchedBy={r.matched_by} />
                    </div>
                    <div className="mt-1.5">
                      <ScoreBar score={r.score} max={max} />
                    </div>
                  </div>
                </div>
              </Link>
            ))
          )}
        </div>
      )}
    </div>
  );
}
