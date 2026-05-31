"""把「测试数据集补充」里的学位论文 XLSX 元数据转换成与 WanFangdata.csv 一致的
13 列期刊 CSV 模式，便于复用现有 csv_meta/ingest 链路。

学位论文字段 → 期刊 CSV 列映射：
  题名→题名, 作者→作者, 关键词→关键词, 摘要→摘要, DOI→DOI,
  学位授予单位→作者单位（即 affiliation）,
  刊名 = "{学位授予单位}（学位论文）,{学位年度}"  → csv_meta 由此解析 source_journal 与 publish_year,
  核心类型="学位论文", 中图分类号→中图分类号

用法：
  python -m scripts.prep_supplement_meta \
    --xlsx 测试数据集补充/2026-05-31下午11-32-39@WanFangdata.xlsx \
    --out  测试数据集补充/theses_meta.csv
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

_REPO_ROOT = Path(__file__).resolve().parent.parent
if str(_REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(_REPO_ROOT))

JOURNAL_COLS = [
    "序号", "题名", "作者", "关键词", "摘要", "DOI", "CN",
    "作者单位", "刊名", "ISSN", "页码", "核心类型", "中图分类号",
]


def convert(xlsx_path: str) -> "list[dict]":
    import pandas as pd

    df = pd.read_excel(xlsx_path, dtype=str).fillna("")
    rows = []
    for _, r in df.iterrows():
        title = str(r.get("题名", "")).strip()
        if not title:
            continue
        unit = str(r.get("学位授予单位", "")).strip()
        year = str(r.get("学位年度", "")).strip()
        source = f"{unit}（学位论文）,{year}" if unit else (f"学位论文,{year}" if year else "")
        rows.append({
            "序号": str(r.get("序号", "")).strip(),
            "题名": title,
            "作者": str(r.get("作者", "")).strip(),
            "关键词": str(r.get("关键词", "")).strip(),
            "摘要": str(r.get("摘要", "")).strip(),
            "DOI": str(r.get("DOI", "")).strip(),
            "CN": "",
            "作者单位": unit,
            "刊名": source,
            "ISSN": "",
            "页码": "",
            "核心类型": "学位论文",
            "中图分类号": str(r.get("中图分类号", "")).strip(),
        })
    return rows


def run(args: argparse.Namespace) -> int:
    import csv

    rows = convert(args.xlsx)
    out = Path(args.out)
    with out.open("w", encoding="gb18030", errors="replace", newline="") as f:
        w = csv.DictWriter(f, fieldnames=JOURNAL_COLS)
        w.writeheader()
        for row in rows:
            w.writerow(row)
    print(f"[prep] wrote {len(rows)} thesis rows -> {out} (gb18030)")

    if args.append_to:
        # 追加到主 CSV（不写表头），保持 gb18030
        target = Path(args.append_to)
        with target.open("a", encoding="gb18030", errors="replace", newline="") as f:
            w = csv.DictWriter(f, fieldnames=JOURNAL_COLS)
            for row in rows:
                w.writerow(row)
        print(f"[prep] appended {len(rows)} rows -> {target}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="python -m scripts.prep_supplement_meta")
    p.add_argument("--xlsx", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--append-to", default=None, help="可选：追加到主 WanFangdata.csv")
    return p


def main(argv=None) -> int:
    return run(build_parser().parse_args(argv))


if __name__ == "__main__":
    raise SystemExit(main())
