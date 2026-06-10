import { FormEvent, useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import type {
  Facets,
  Hit,
  SearchField,
  SearchFilters,
  SmartSearchResponse,
  TraditionalSearchRequest,
  TraditionalSearchResponse,
} from "../api/types";

const FIELD_OPTIONS: { value: SearchField; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "theme", label: "主题" },
  { value: "title_or_keywords", label: "题名或关键词" },
  { value: "title", label: "题名" },
  { value: "first_author", label: "第一作者" },
  { value: "author", label: "作者" },
  { value: "affiliation", label: "作者单位" },
  { value: "keywords", label: "关键词" },
  { value: "abstract", label: "摘要" },
  { value: "doi", label: "DOI" },
];
import { HttpError } from "../api/client";
import SectionTitle from "../components/SectionTitle";
import ResultRow from "../components/ResultRow";
import Loading from "../components/Loading";
import FacetSidebar, {
  FacetSelection,
  emptySelection,
  selectionEmpty,
  selectionToFilters,
  toggleSelection,
} from "../components/FacetSidebar";

type Tab = "traditional" | "smart";

export default function Search() {
  const [tab, setTab] = useState<Tab>("traditional");
  // 智能检索的「以此检索式重新检索」→ 切到传统模式并预填布尔检索式
  const [boolPrefill, setBoolPrefill] = useState<string | null>(null);

  function useBooleanQuery(expr: string) {
    setBoolPrefill(expr);
    setTab("traditional");
  }

  return (
    <div className="space-y-6">
      <div>
        <div className="kicker mb-3">// retrieval</div>
        <h1 className="font-display font-bold text-2xl">文献检索</h1>
      </div>
      <div className="flex gap-1 border-b border-line">
        <button
          className={`tab-term ${tab === "traditional" ? "tab-active" : ""}`}
          onClick={() => setTab("traditional")}
        >
          文献检索 · indexing
        </button>
        <button
          className={`tab-term ${tab === "smart" ? "tab-active" : ""}`}
          onClick={() => setTab("smart")}
        >
          AI 增强检索 · generative
        </button>
      </div>
      {tab === "traditional" ? (
        <Traditional prefillQuery={boolPrefill} onPrefillConsumed={() => setBoolPrefill(null)} />
      ) : (
        <Smart onUseBooleanQuery={useBooleanQuery} />
      )}
    </div>
  );
}

/* ----------------------- 传统检索 ----------------------- */

function Traditional({
  prefillQuery,
  onPrefillConsumed,
}: {
  prefillQuery: string | null;
  onPrefillConsumed: () => void;
}) {
  const [q, setQ] = useState("");
  const [field, setField] = useState<SearchField>("all");
  const [sort, setSort] = useState<"relevance" | "year">("relevance");
  const [page, setPage] = useState(1);
  const pageSize = 10;

  // 高级检索面板
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [advAuthor, setAdvAuthor] = useState("");
  const [advAffiliation, setAdvAffiliation] = useState("");
  const [advJournal, setAdvJournal] = useState("");
  const [advClc, setAdvClc] = useState("");
  const [advCore, setAdvCore] = useState<"" | "yes" | "no">("");
  const [yearFrom, setYearFrom] = useState("");
  const [yearTo, setYearTo] = useState("");

  // 侧边栏 facet 多选
  const [selection, setSelection] = useState<FacetSelection>(emptySelection());

  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [resp, setResp] = useState<TraditionalSearchResponse | null>(null);

  function buildFilters(sel: FacetSelection): SearchFilters | undefined {
    const f: SearchFilters = { ...(selectionToFilters(sel) ?? {}) };
    if (advAuthor.trim()) f.authors = [...(f.authors ?? []), advAuthor.trim()];
    if (advAffiliation.trim()) f.affiliations = [...(f.affiliations ?? []), advAffiliation.trim()];
    if (advJournal.trim()) f.journals = [...(f.journals ?? []), advJournal.trim()];
    if (advClc.trim()) f.clcs = [...(f.clcs ?? []), advClc.trim()];
    if (advCore && f.is_core === undefined) f.is_core = advCore === "yes";
    const yf = Number(yearFrom.trim());
    const yt = Number(yearTo.trim());
    if (yearFrom.trim() && Number.isFinite(yf)) f.year_from = yf;
    if (yearTo.trim() && Number.isFinite(yt)) f.year_to = yt;
    return Object.keys(f).length ? f : undefined;
  }

  async function run(p = page, sel = selection) {
    setLoading(true);
    setErr(null);
    try {
      const body: TraditionalSearchRequest = {
        q: q || undefined,
        field,
        sort,
        page: p,
        page_size: pageSize,
        filters: buildFilters(sel),
      };
      const data = await api.searchTraditional(body);
      setResp(data);
      setPage(p);
    } catch (e) {
      setErr(e instanceof HttpError ? e.message : String(e));
      setResp(null);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (prefillQuery) {
      setQ(prefillQuery);
      onPrefillConsumed();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prefillQuery]);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    setSelection(emptySelection());
    run(1, emptySelection());
  }

  function onFacetToggle(key: keyof Facets, value: string) {
    const next = toggleSelection(selection, key, value);
    setSelection(next);
    run(1, next);
  }

  function onFacetClear() {
    setSelection(emptySelection());
    run(1, emptySelection());
  }

  const scoreMax = useMemo(
    () => (resp?.hits ?? []).reduce((m, h) => Math.max(m, h.score), 1),
    [resp],
  );
  const total = resp?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const placeholder =
    field === "doi"
      ? "10.xxxx/xxxx"
      : field === "affiliation"
        ? "如：北京大学"
        : field === "first_author" || field === "author"
          ? "作者姓名"
          : '支持布尔检索：(深度学习 OR 机器学习) AND NOT 综述 · "精确短语" · title:数字治理';

  const activeChips: { label: string; clear: () => void }[] = [];
  selection.authors.forEach((a) =>
    activeChips.push({ label: `作者: ${a}`, clear: () => onFacetToggle("authors", a) }),
  );
  selection.journals.forEach((j) =>
    activeChips.push({ label: `刊名: ${j}`, clear: () => onFacetToggle("journals", j) }),
  );
  selection.years.forEach((y) =>
    activeChips.push({ label: `年份: ${y}`, clear: () => onFacetToggle("years", String(y)) }),
  );
  selection.clcs.forEach((c) =>
    activeChips.push({ label: `分类: ${c}`, clear: () => onFacetToggle("clc", c) }),
  );
  selection.affiliations.forEach((a) =>
    activeChips.push({ label: `单位: ${a}`, clear: () => onFacetToggle("affiliations", a) }),
  );
  if (selection.isCore !== null) {
    activeChips.push({
      label: selection.isCore ? "核心期刊" : "非核心",
      clear: () => onFacetToggle("core", selection.isCore ? "核心期刊" : "非核心"),
    });
  }

  return (
    <div className="space-y-5">
      {/* 顶部检索栏 */}
      <form onSubmit={onSubmit} className="term-panel p-4 space-y-3">
        <div className="flex flex-col md:flex-row gap-2">
          <select
            value={field}
            onChange={(e) => setField(e.target.value as SearchField)}
            className="input-term md:w-40 shrink-0"
            aria-label="检索字段"
          >
            {FIELD_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder={placeholder}
            className="input-term flex-1"
          />
          <div className="flex gap-2 shrink-0">
            <button type="submit" className="btn-term btn-primary" disabled={loading}>
              {loading ? "searching…" : "▸ 检索"}
            </button>
            <button
              type="button"
              className="btn-term"
              onClick={() => setShowAdvanced((v) => !v)}
            >
              高级 {showAdvanced ? "▾" : "▸"}
            </button>
          </div>
        </div>

        {showAdvanced && (
          <div className="fade-in border-t border-line pt-3 space-y-3">
            <div className="kicker">
              advanced · 高级检索（检索式支持 AND / OR / NOT / 括号 / "短语" / title: 字段限定）
            </div>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
              <Field label="author · 作者" value={advAuthor} set={setAdvAuthor} />
              <Field label="affiliation · 作者单位" value={advAffiliation} set={setAdvAffiliation} />
              <Field label="journal · 刊名" value={advJournal} set={setAdvJournal} />
              <Field label="clc · 中图分类号" value={advClc} set={setAdvClc} mono placeholder="TP391" />
              <Field label="from · 起始年" value={yearFrom} set={setYearFrom} mono placeholder="2020" />
              <Field label="to · 截止年" value={yearTo} set={setYearTo} mono placeholder="2025" />
            </div>
            <div className="flex flex-wrap items-center gap-4">
              <div className="flex items-center gap-2">
                <span className="kicker">核心期刊</span>
                {([
                  ["", "不限"],
                  ["yes", "是"],
                  ["no", "否"],
                ] as const).map(([v, lbl]) => (
                  <button
                    type="button"
                    key={v}
                    onClick={() => setAdvCore(v)}
                    className={`chip ${advCore === v ? "chip-cyan" : ""}`}
                  >
                    {lbl}
                  </button>
                ))}
              </div>
              <div className="flex items-center gap-2">
                <span className="kicker">sort · 排序</span>
                {(["relevance", "year"] as const).map((s) => (
                  <button
                    type="button"
                    key={s}
                    onClick={() => setSort(s)}
                    className={`chip ${sort === s ? "chip-cyan" : ""}`}
                  >
                    {s === "relevance" ? "相关性" : "发表时间"}
                  </button>
                ))}
              </div>
            </div>
          </div>
        )}
      </form>

      <div className="grid grid-cols-12 gap-6">
        {/* facet 侧边栏 */}
        <aside className="col-span-12 md:col-span-4 lg:col-span-3 space-y-3 md:sticky md:top-6 self-start">
          {resp ? (
            <FacetSidebar
              facets={resp.facets}
              selection={selection}
              onToggle={onFacetToggle}
              onClear={onFacetClear}
            />
          ) : (
            <div className="term-panel p-4 text-text-3 text-sm italic">
              检索后这里显示作者 / 刊名 / 年份 / 核心期刊 / 中图分类等筛选项
            </div>
          )}
        </aside>

        {/* results */}
        <section className="col-span-12 md:col-span-8 lg:col-span-9">
          <SectionTitle
            right={
              resp ? (
                <span className="mono text-xs text-text-3 tnum">
                  {total} hits · p{page}/{totalPages}
                  {resp.boolean_mode ? " · boolean" : ""}
                </span>
              ) : null
            }
          >
            results · 检索结果
          </SectionTitle>
          {activeChips.length > 0 && (
            <div className="mb-3 flex flex-wrap items-center gap-1.5">
              <span className="kicker">已选:</span>
              {activeChips.map((c) => (
                <button key={c.label} onClick={c.clear} className="chip chip-cyan" title="点击移除">
                  {c.label} ✕
                </button>
              ))}
            </div>
          )}
          {loading && <Loading label="SEARCHING" />}
          {err && <ErrorBox msg={err} />}
          {!loading && !err && !resp && <Empty text="填入条件后点击「检索」" />}
          {resp && resp.hits.length === 0 && !loading && <Empty text="无结果 · no hits" />}
          {resp && resp.hits.length > 0 && (
            <div className="space-y-2 stagger">
              {resp.hits.map((h, i) => (
                <ResultRow
                  key={h.paper_id + i}
                  hit={h}
                  index={(page - 1) * pageSize + i + 1}
                  scoreMax={scoreMax}
                />
              ))}
            </div>
          )}
          {resp && totalPages > 1 && (
            <div className="mt-5 flex items-center justify-center gap-3">
              <button className="btn-term" disabled={page <= 1 || loading} onClick={() => run(page - 1)}>
                ← prev
              </button>
              <span className="mono text-xs tnum text-text-2">
                {String(page).padStart(2, "0")} / {String(totalPages).padStart(2, "0")}
              </span>
              <button className="btn-term" disabled={page >= totalPages || loading} onClick={() => run(page + 1)}>
                next →
              </button>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

/* ----------------------- AI 增强检索 ----------------------- */

function Smart({ onUseBooleanQuery }: { onUseBooleanQuery: (expr: string) => void }) {
  const [q, setQ] = useState("");
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [resp, setResp] = useState<SmartSearchResponse | null>(null);
  const [list, setList] = useState<"golden" | "bm25" | "vector">("golden");
  const [selection, setSelection] = useState<FacetSelection>(emptySelection());

  async function doSearch(sel: FacetSelection) {
    if (!q.trim()) return;
    setLoading(true);
    setErr(null);
    try {
      setResp(await api.searchSmart({ q: q.trim(), filters: selectionToFilters(sel) }));
    } catch (e2) {
      setErr(e2 instanceof HttpError ? e2.message : String(e2));
      setResp(null);
    } finally {
      setLoading(false);
    }
  }

  async function run(e: FormEvent) {
    e.preventDefault();
    setSelection(emptySelection());
    await doSearch(emptySelection());
  }

  function onFacetToggle(key: keyof Facets, value: string) {
    const next = toggleSelection(selection, key, value);
    setSelection(next);
    void doSearch(next);
  }

  function onFacetClear() {
    setSelection(emptySelection());
    void doSearch(emptySelection());
  }

  const hits: Hit[] =
    resp == null
      ? []
      : list === "golden"
        ? resp.golden
        : list === "bm25"
          ? resp.list_bm25
          : resp.list_vector;
  const scoreMax = useMemo(() => hits.reduce((m, h) => Math.max(m, h.score), 0.0001), [hits]);
  const payload = resp?.rewrite?.search_payload;
  const booleanQuery = resp?.boolean_query || resp?.rewrite?.boolean_query || "";

  return (
    <div className="space-y-5">
      <form onSubmit={run} className="term-panel p-4">
        <div className="kicker mb-2">prompt · 自然语言研究问题</div>
        <textarea
          value={q}
          onChange={(e) => setQ(e.target.value)}
          rows={3}
          placeholder="例如：数字时代文化传播的异化与超越"
          className="input-term resize-y font-sans"
        />
        <div className="mt-3 flex items-center gap-3">
          <button type="submit" className="btn-term btn-primary" disabled={loading || !q.trim()}>
            {loading ? "analyzing…" : "▸ 智能检索"}
          </button>
          <span className="mono text-[11px] text-text-3">rewrite → BM25 ∪ vector → RRF</span>
        </div>
      </form>

      {loading && <Loading label="REWRITING & RETRIEVING" />}
      {err && <ErrorBox msg={err} />}

      {resp && payload && (
        <div className="term-panel p-4">
          <div className="kicker mb-3">query rewrite · 查询包</div>
          {payload.core_semantic_sentence && (
            <p className="text-sm text-text mb-3">
              <span className="chip chip-cyan mr-2">core</span>
              {payload.core_semantic_sentence}
            </p>
          )}
          {booleanQuery && (
            <div className="mb-3 flex flex-wrap items-center gap-2">
              <span className="chip chip-violet">检索式</span>
              <code className="mono text-[13px] text-cyan break-all">{booleanQuery}</code>
              <button
                type="button"
                className="kicker text-text-3 hover:text-cyan transition-colors"
                onClick={() => onUseBooleanQuery(booleanQuery)}
              >
                ▸ 以此检索式重新检索
              </button>
            </div>
          )}
          <RewriteRow label="academic" tone="amber" words={payload.academic_keywords} />
          <RewriteRow label="synonyms" tone="default" words={payload.synonyms_and_extensions} />
          <RewriteRow label="variables" tone="default" words={payload.potential_variables} />
          <RewriteRow label="design" tone="violet" words={payload.research_design_terms} />
        </div>
      )}

      {resp && (
        <div className="grid grid-cols-12 gap-6">
          <aside className="col-span-12 md:col-span-4 lg:col-span-3 md:sticky md:top-6 self-start">
            <FacetSidebar
              facets={resp.facets}
              selection={selection}
              onToggle={onFacetToggle}
              onClear={onFacetClear}
            />
          </aside>
          <div className="col-span-12 md:col-span-8 lg:col-span-9">
            <div className="flex gap-1 border-b border-line mb-3">
              {([
                ["golden", "综合榜"],
                ["bm25", "关键词"],
                ["vector", "语义"],
              ] as const).map(([k, lbl]) => (
                <button
                  key={k}
                  className={`tab-term ${list === k ? "tab-active" : ""}`}
                  onClick={() => setList(k)}
                >
                  {lbl}
                </button>
              ))}
            </div>
            {hits.length === 0 ? (
              <Empty text={list === "vector" ? "语义榜为空（确认向量已嵌入）" : "本榜单无结果"} />
            ) : (
              <div className="space-y-2 stagger">
                {hits.map((h, i) => (
                  <ResultRow key={h.paper_id + i} hit={h} index={i + 1} scoreMax={scoreMax} />
                ))}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function RewriteRow({
  label,
  words,
  tone,
}: {
  label: string;
  words: string[];
  tone: "amber" | "violet" | "default";
}) {
  if (!words?.length) return null;
  const cls = tone === "amber" ? "chip-amber" : tone === "violet" ? "chip-violet" : "";
  return (
    <div className="flex items-start gap-3 py-1.5 border-t border-line first:border-t-0">
      <span className="kicker w-20 shrink-0 pt-1">{label}</span>
      <div className="flex flex-wrap gap-1.5">
        {words.map((w) => (
          <span key={w} className={`chip ${cls}`}>
            {w}
          </span>
        ))}
      </div>
    </div>
  );
}

/* ----------------------- shared ----------------------- */

function Field({
  label,
  value,
  set,
  placeholder,
  mono,
}: {
  label: string;
  value: string;
  set: (v: string) => void;
  placeholder?: string;
  mono?: boolean;
}) {
  return (
    <label className="block">
      <span className="kicker">{label}</span>
      <input
        value={value}
        onChange={(e) => set(e.target.value)}
        placeholder={placeholder}
        className={`input-term mt-1 ${mono ? "mono tnum" : ""}`}
      />
    </label>
  );
}

function Empty({ text }: { text: string }) {
  return (
    <div className="term-panel py-12 text-center text-text-3 text-sm italic">— {text} —</div>
  );
}

function ErrorBox({ msg }: { msg: string }) {
  return (
    <div className="term-panel border-red px-4 py-3 mono text-sm text-red" style={{ borderColor: "var(--red)" }}>
      ERROR · {msg}
    </div>
  );
}
