import type { Facets, SearchFilters } from "../api/types";

// 知网式侧边栏筛选：每个字段列出候选值与命中数（降序），
// 点击切换选中（同字段多值 OR），选中后由父组件重新发起检索。
const GROUPS: { key: keyof Facets; label: string }[] = [
  { key: "core", label: "核心期刊" },
  { key: "authors", label: "作者" },
  { key: "journals", label: "刊名" },
  { key: "years", label: "发表年份" },
  { key: "clc", label: "中图分类" },
  { key: "affiliations", label: "作者单位" },
];

export interface FacetSelection {
  authors: string[];
  journals: string[];
  affiliations: string[];
  clcs: string[];
  years: number[];
  isCore: boolean | null;
}

export const emptySelection = (): FacetSelection => ({
  authors: [],
  journals: [],
  affiliations: [],
  clcs: [],
  years: [],
  isCore: null,
});

export function selectionEmpty(sel: FacetSelection): boolean {
  return (
    sel.authors.length === 0 &&
    sel.journals.length === 0 &&
    sel.affiliations.length === 0 &&
    sel.clcs.length === 0 &&
    sel.years.length === 0 &&
    sel.isCore === null
  );
}

export function selectionToFilters(sel: FacetSelection): SearchFilters | undefined {
  if (selectionEmpty(sel)) return undefined;
  return {
    authors: sel.authors.length ? sel.authors : undefined,
    journals: sel.journals.length ? sel.journals : undefined,
    affiliations: sel.affiliations.length ? sel.affiliations : undefined,
    clcs: sel.clcs.length ? sel.clcs : undefined,
    years: sel.years.length ? sel.years : undefined,
    is_core: sel.isCore === null ? undefined : sel.isCore,
  };
}

function isSelected(sel: FacetSelection, key: keyof Facets, value: string): boolean {
  switch (key) {
    case "authors":
      return sel.authors.includes(value);
    case "journals":
      return sel.journals.includes(value);
    case "affiliations":
      return sel.affiliations.includes(value);
    case "clc":
      return sel.clcs.includes(value);
    case "years":
      return sel.years.includes(Number(value));
    case "core":
      return sel.isCore !== null && (value === "核心期刊") === sel.isCore;
  }
}

export function toggleSelection(sel: FacetSelection, key: keyof Facets, value: string): FacetSelection {
  const flip = (arr: string[]) =>
    arr.includes(value) ? arr.filter((x) => x !== value) : [...arr, value];
  switch (key) {
    case "authors":
      return { ...sel, authors: flip(sel.authors) };
    case "journals":
      return { ...sel, journals: flip(sel.journals) };
    case "affiliations":
      return { ...sel, affiliations: flip(sel.affiliations) };
    case "clc":
      return { ...sel, clcs: flip(sel.clcs) };
    case "years": {
      const y = Number(value);
      return {
        ...sel,
        years: sel.years.includes(y) ? sel.years.filter((x) => x !== y) : [...sel.years, y],
      };
    }
    case "core": {
      const want = value === "核心期刊";
      return { ...sel, isCore: sel.isCore === want ? null : want };
    }
  }
}

export default function FacetSidebar({
  facets,
  selection,
  onToggle,
  onClear,
}: {
  facets: Facets | undefined;
  selection: FacetSelection;
  onToggle: (key: keyof Facets, value: string) => void;
  onClear: () => void;
}) {
  if (!facets) return null;
  const hasAny = GROUPS.some((g) => (facets[g.key] ?? []).length > 0);
  if (!hasAny) return null;
  return (
    <div className="term-panel p-4 space-y-4">
      <div className="flex items-center justify-between">
        <span className="kicker">facets · 结果筛选</span>
        {!selectionEmpty(selection) && (
          <button onClick={onClear} className="kicker text-text-3 hover:text-red transition-colors">
            清除
          </button>
        )}
      </div>
      {GROUPS.map((g) => {
        const items = facets[g.key] ?? [];
        if (items.length === 0) return null;
        return (
          <div key={g.key}>
            <div className="kicker mb-1.5">{g.label}</div>
            <div className="space-y-0.5 max-h-48 overflow-y-auto pr-1">
              {items.map((it) => {
                const sel = isSelected(selection, g.key, it.value);
                return (
                  <button
                    key={it.value}
                    onClick={() => onToggle(g.key, it.value)}
                    className={`w-full flex items-center justify-between gap-2 px-1.5 py-1 text-left text-[13px] transition-colors ${
                      sel ? "text-cyan" : "text-text-2 hover:text-text"
                    }`}
                    title={it.value}
                  >
                    <span className="truncate">
                      <span className="mono text-[11px] mr-1">{sel ? "☑" : "☐"}</span>
                      {it.value}
                    </span>
                    <span className="mono text-[11px] tnum text-text-3 shrink-0">({it.count})</span>
                  </button>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}
