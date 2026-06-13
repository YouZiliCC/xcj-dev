import { FormEvent, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import type { Components } from "react-markdown";
import { api, HttpError, StreamHandlers } from "../api/client";
import type { ChunkCitation, QaMeta, Reference } from "../api/types";
import SectionTitle from "../components/SectionTitle";
import Loading from "../components/Loading";
import Markdown from "../components/Markdown";
import MatchChips from "../components/MatchChips";

// 五段固定结构（与 pyservice QA Prompt、修改设计.md 对齐）
const SECTION_ORDER = ["概念解释", "背景说明", "方法依据", "经验证据", "研究空白"] as const;

interface ParsedSection {
  role: string;
  body: string;
}

// 把流式累积的 markdown 按「## 角色名」切成段（容忍未完成的尾部）
function parseSections(answer: string): { preamble: string; sections: ParsedSection[] } {
  const re = /^##\s*(概念解释|背景说明|方法依据|经验证据|研究空白)[：:\s]*$/gm;
  const matches = [...answer.matchAll(re)];
  if (matches.length === 0) return { preamble: answer, sections: [] };
  const preamble = answer.slice(0, matches[0].index).trim();
  const sections: ParsedSection[] = matches.map((m, i) => {
    const start = (m.index ?? 0) + m[0].length;
    const end = i + 1 < matches.length ? matches[i + 1].index : answer.length;
    return { role: m[1], body: answer.slice(start, end).trim() };
  });
  return { preamble, sections };
}

// 把正文中的 [n] 引用替换成 markdown 链接，供 components 渲染为可点击角标
function linkifyCitations(body: string): string {
  return body.replace(/\[(\d{1,3})\]/g, (_m, n) => `[${n}](#cite-${n})`);
}

export default function QA() {
  const [question, setQuestion] = useState("");
  const [author, setAuthor] = useState("");
  const [year, setYear] = useState("");
  const [journal, setJournal] = useState("");
  const [showFilters, setShowFilters] = useState(false);

  const [answer, setAnswer] = useState("");
  const [references, setReferences] = useState<Reference[]>([]);
  const [citations, setCitations] = useState<ChunkCitation[]>([]);
  const [activeRef, setActiveRef] = useState<number | null>(null);
  const [evidenceSufficient, setEvidenceSufficient] = useState(true);
  const [loading, setLoading] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [hasResult, setHasResult] = useState(false);

  async function run(e: FormEvent) {
    e.preventDefault();
    if (!question.trim()) return;
    setAnswer("");
    setReferences([]);
    setCitations([]);
    setActiveRef(null);
    setEvidenceSufficient(true);
    setErr(null);
    setLoading(true);
    setStreaming(true);
    setHasResult(true);
    try {
      const yn = year.trim() ? Number(year.trim()) : undefined;
      const filters =
        author.trim() || journal.trim() || (yn && Number.isFinite(yn))
          ? {
              author: author.trim() || undefined,
              journal: journal.trim() || undefined,
              publish_year: yn && Number.isFinite(yn) ? yn : undefined,
            }
          : undefined;
      const handlers: StreamHandlers<QaMeta> = {
        onMeta: (m) => {
          setReferences(m.references ?? []);
          const cs = m.citations ?? [];
          setCitations(cs);
          // 默认展示第一条引用，无需用户点击角标
          if (cs.length > 0) setActiveRef(cs[0].ref_id);
          setEvidenceSufficient(m.evidence_sufficient ?? true);
        },
        onDelta: (t) => {
          setLoading(false);
          setAnswer((a) => a + t);
        },
        onError: (msg) => setErr(msg),
      };
      await api.qaAnswerStream({ question: question.trim(), filters }, handlers);
    } catch (e2) {
      setErr(e2 instanceof HttpError ? e2.message : String(e2));
    } finally {
      setLoading(false);
      setStreaming(false);
    }
  }

  const { preamble, sections } = useMemo(() => parseSections(answer), [answer]);
  const citationByRef = useMemo(() => {
    const m = new Map<number, ChunkCitation>();
    citations.forEach((c) => m.set(c.ref_id, c));
    return m;
  }, [citations]);
  const paperCount = useMemo(() => new Set(citations.map((c) => c.paper_id)).size, [citations]);
  const active = activeRef !== null ? citationByRef.get(activeRef) : undefined;

  // [n] 链接 → 可点击角标 chip
  const mdComponents: Components = useMemo(
    () => ({
      a: ({ href, children }) => {
        if (href?.startsWith("#cite-")) {
          const n = Number(href.slice(6));
          const known = citationByRef.has(n);
          return (
            <button
              type="button"
              onClick={() => known && setActiveRef(n)}
              className={`mono text-[11px] align-super px-1 border transition-colors ${
                known
                  ? "text-cyan border-line-2 hover:border-cyan cursor-pointer"
                  : "text-text-3 border-line cursor-default"
              }`}
              title={known ? citationByRef.get(n)?.title : undefined}
            >
              {n}
            </button>
          );
        }
        return (
          <a href={href} target="_blank" rel="noreferrer">
            {children}
          </a>
        );
      },
    }),
    [citationByRef],
  );

  return (
    <div className="space-y-6">
      <div>
        <div className="kicker mb-3">// rag-qa</div>
        <h1 className="font-display font-bold text-2xl">学术研究 · 智能问答</h1>
        <p className="mt-2 text-text-2 text-sm max-w-2xl leading-relaxed">
          提出一个研究问题，系统检索证据文献后按「概念解释 / 背景说明 / 方法依据 / 经验证据 /
          研究空白」五段固定结构回答，每段标注可点击的文本块级参考文献。
        </p>
      </div>

      <form onSubmit={run} className="term-panel p-4">
        <div className="kicker mb-2">question · 你的问题</div>
        <textarea
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          rows={3}
          placeholder="例如：数智时代文化生产为什么会出现异化？有哪些超越路径？"
          className="input-term resize-y font-sans"
        />
        <div className="mt-3 flex flex-wrap items-center gap-3">
          <button type="submit" className="btn-term btn-primary" disabled={streaming || !question.trim()}>
            {streaming ? "answering…" : "▸ 提问"}
          </button>
          <button
            type="button"
            className="kicker text-text-3 hover:text-cyan transition-colors"
            onClick={() => setShowFilters((v) => !v)}
          >
            {showFilters ? "▾ 收起过滤" : "▸ 可选过滤"}
          </button>
        </div>
        {showFilters && (
          <div className="mt-3 grid grid-cols-3 gap-3 fade-in">
            <label className="block">
              <span className="kicker">author · 作者</span>
              <input value={author} onChange={(e) => setAuthor(e.target.value)} className="input-term mt-1" />
            </label>
            <label className="block">
              <span className="kicker">journal · 刊名</span>
              <input value={journal} onChange={(e) => setJournal(e.target.value)} className="input-term mt-1" />
            </label>
            <label className="block">
              <span className="kicker">year · 年份</span>
              <input value={year} onChange={(e) => setYear(e.target.value)} className="input-term mt-1 mono tnum" placeholder="2024" />
            </label>
          </div>
        )}
      </form>

      {loading && <Loading label="RETRIEVING & REASONING" />}
      {err && (
        <div className="term-panel px-4 py-3 mono text-sm text-red" style={{ borderColor: "var(--red)" }}>
          ERROR · {err}
        </div>
      )}

      {hasResult && (
        <>
          {!evidenceSufficient && (
            <div
              className="term-panel px-4 py-3 text-sm flex items-center gap-2"
              style={{ borderColor: "var(--red)", color: "var(--red)" }}
            >
              <span className="mono">⚠</span> 检索到的相关文献较少，以下结论可信度有限。
            </div>
          )}

          {(answer || (streaming && !loading)) && (
            <section>
              <SectionTitle
                right={
                  citations.length > 0 ? (
                    <span className="mono text-xs text-text-3 tnum">
                      共引用 {citations.length} 个文本块 · 来自 {paperCount} 篇论文
                    </span>
                  ) : null
                }
              >
                answer · 回答
              </SectionTitle>
              <div className="grid grid-cols-12 gap-6">
                <div className="col-span-12 lg:col-span-8 space-y-3">
                  {preamble && (
                    <div className="term-panel p-4 text-sm text-text-2">
                      <Markdown source={linkifyCitations(preamble)} components={mdComponents} />
                    </div>
                  )}
                  {sections.length === 0 && answer && !preamble && (
                    <div className="term-panel p-5">
                      <Markdown source={linkifyCitations(answer)} components={mdComponents} />
                    </div>
                  )}
                  {sections.map((s, i) => (
                    <AnswerSection
                      key={s.role}
                      section={s}
                      components={mdComponents}
                      streamingCursor={streaming && i === sections.length - 1}
                    />
                  ))}
                  {streaming && sections.length === 0 && !answer && (
                    <span className="animate-pulse text-cyan">▍</span>
                  )}
                </div>

                {/* 引用侧栏 */}
                <aside className="col-span-12 lg:col-span-4">
                  <div className="lg:sticky lg:top-6">
                    {active ? (
                      <CitationPanel c={active} onClose={() => setActiveRef(null)} />
                    ) : citations.length > 0 ? (
                      <div className="term-panel p-4 text-sm text-text-3 italic">
                        点击正文中的 [n] 角标查看对应文本块原文与来源论文。
                      </div>
                    ) : null}
                  </div>
                </aside>
              </div>
            </section>
          )}

          {references.length > 0 && (
            <section>
              <SectionTitle>
                <span>references · 参考文献表</span>
              </SectionTitle>
              <div className="term-panel overflow-x-auto">
                <table className="w-full text-sm border-collapse">
                  <thead>
                    <tr className="text-left">
                      {["#", "标题", "作者", "年份", "命中", "证据"].map((h) => (
                        <th
                          key={h}
                          className="mono text-[11px] uppercase tracking-wide text-text-3 px-3 py-2 border-b border-line-2 whitespace-nowrap"
                        >
                          {h}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {references.map((r) => (
                      <ReferenceRow key={r.paper_id + r.rank} r={r} />
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
        </>
      )}
    </div>
  );
}

function AnswerSection({
  section,
  components,
  streamingCursor,
}: {
  section: ParsedSection;
  components: Components;
  streamingCursor: boolean;
}) {
  const [open, setOpen] = useState(true);
  const idx = SECTION_ORDER.indexOf(section.role as (typeof SECTION_ORDER)[number]);
  const noEvidence = section.body.includes("暂无相关文献证据");
  return (
    <div className="term-panel">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="w-full flex items-center justify-between px-4 py-2.5 text-left hover:bg-bg-2 transition-colors"
      >
        <span className="flex items-center gap-2">
          <span className="mono text-[11px] text-amber tnum">{String(idx + 1).padStart(2, "0")}</span>
          <span className="font-display font-semibold text-sm">{section.role}</span>
          {noEvidence && <span className="chip text-text-3">暂无证据</span>}
        </span>
        <span className="kicker text-text-3">{open ? "▾" : "▸"}</span>
      </button>
      {open && (
        <div className="px-4 pb-4 border-t border-line pt-3">
          <Markdown source={linkifyCitations(section.body)} components={components} />
          {streamingCursor && <span className="animate-pulse text-cyan">▍</span>}
        </div>
      )}
    </div>
  );
}

function CitationPanel({ c, onClose }: { c: ChunkCitation; onClose: () => void }) {
  return (
    <div className="term-panel p-4 space-y-3 fade-in">
      <div className="flex items-center justify-between">
        <span className="kicker">
          citation · 引用 [{c.ref_id}]
        </span>
        <button onClick={onClose} className="kicker text-text-3 hover:text-red transition-colors">
          ✕
        </button>
      </div>
      <div className="flex flex-wrap gap-1.5">
        {c.section_role && <span className="chip chip-violet">{c.section_role}</span>}
        {c.chapter_title && <span className="chip">{c.chapter_title}</span>}
      </div>
      <p className="text-[13px] leading-relaxed text-text-2 border-l-2 border-line-2 pl-3 whitespace-pre-wrap max-h-72 overflow-y-auto">
        {c.text}
      </p>
      <div className="border-t border-line pt-3 space-y-1 text-sm">
        <Link
          to={`/paper/${encodeURIComponent(c.paper_id)}`}
          className="block text-text hover:text-cyan transition-colors"
        >
          {c.title}
        </Link>
        <div className="text-text-3 text-[12px]">
          {[c.author, c.journal, c.year > 0 ? c.year : ""].filter(Boolean).join(" · ")}
        </div>
        {c.doi && <div className="mono text-[11px] text-text-3">{c.doi}</div>}
      </div>
    </div>
  );
}

function ReferenceRow({ r }: { r: Reference }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <tr className="border-b border-line align-top hover:bg-bg-2 transition-colors">
        <td className="mono text-amber tnum px-3 py-2.5">{String(r.rank).padStart(2, "0")}</td>
        <td className="px-3 py-2.5 min-w-[220px]">
          <Link
            to={`/paper/${encodeURIComponent(r.paper_id)}`}
            className="text-text hover:text-cyan transition-colors"
          >
            {r.title}
          </Link>
          {r.doi && <div className="mono text-[11px] text-text-3 mt-0.5">{r.doi}</div>}
        </td>
        <td className="px-3 py-2.5 text-text-2 whitespace-nowrap">{r.author || "—"}</td>
        <td className="px-3 py-2.5 mono tnum text-amber">{r.year || "—"}</td>
        <td className="px-3 py-2.5">
          <MatchChips matchedBy={r.matched_by} />
        </td>
        <td className="px-3 py-2.5">
          {r.snippet ? (
            <button onClick={() => setOpen((v) => !v)} className="kicker text-cyan hover:text-text">
              {open ? "▾" : "▸"}
            </button>
          ) : (
            <span className="text-text-3">—</span>
          )}
        </td>
      </tr>
      {open && r.snippet && (
        <tr className="border-b border-line">
          <td />
          <td colSpan={5} className="px-3 pb-3">
            <p className="fade-in text-[13px] leading-relaxed text-text-2 border-l-2 border-line-2 pl-3 whitespace-pre-wrap">
              {r.snippet}
            </p>
          </td>
        </tr>
      )}
    </>
  );
}
