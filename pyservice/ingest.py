"""数据摄取 CLI：把 CSV 元数据 + Word 全文 → SQLite。

用法示例：
    python -m pyservice.ingest --data-dir /Users/deeryou/xcj-dev/data --no-embed
    python -m pyservice.ingest --limit 20
"""

from __future__ import annotations

import argparse
import os
import re
import sys
import traceback
from pathlib import Path
from typing import Any, Dict, List, Optional

from . import db as dbmod
from . import vector_codec
from .parser import chunk_paper, parse_docx
from .tokenize_cn import tokenize_join

DEFAULT_DB = "./data/storage/papers.db"
DEFAULT_DATA_DIR = "./data"


_PUNCT_RE = re.compile(r"[\s　_\-—_·•．\.,，。:：;；!！\?？“”\"'‘’()（）\[\]【】《》<>/\\|]+")


def _clean(s: Optional[str]) -> str:
    if not s:
        return ""
    return _PUNCT_RE.sub("", str(s)).strip().lower()


def _derive_paper_id(docx_path: Path) -> str:
    stem = docx_path.stem
    # 去掉首尾下划线
    return stem.strip("_")


def _build_csv_indexes(df) -> Dict[str, Any]:
    """构建 title 清洗后 → 行 索引；以及 serial → 行 索引。"""
    by_title: Dict[str, Dict[str, Any]] = {}
    by_serial: Dict[str, Dict[str, Any]] = {}
    by_cleaned_id: Dict[str, Dict[str, Any]] = {}
    for _, row in df.iterrows():
        record = row.to_dict()
        title = _clean(record.get("title"))
        if title and title not in by_title:
            by_title[title] = record
        serial = str(record.get("serial") or "").strip()
        if serial and serial not in by_serial:
            by_serial[serial] = record
        # cleaned title 也用于按文件名匹配
        if title:
            by_cleaned_id[title] = record
    return {"by_title": by_title, "by_serial": by_serial, "by_cleaned_id": by_cleaned_id}


def _lookup_meta(idx: Dict[str, Any], paper_id: str, title_hint: str = "") -> Dict[str, Any]:
    by_title = idx["by_title"]
    by_cleaned_id = idx["by_cleaned_id"]

    cleaned_id = _clean(paper_id)
    cleaned_title = _clean(title_hint) if title_hint else ""

    if cleaned_title and cleaned_title in by_title:
        return by_title[cleaned_title]
    if cleaned_id and cleaned_id in by_cleaned_id:
        return by_cleaned_id[cleaned_id]
    # 模糊匹配：以 cleaned_id 为子串去找 title
    if cleaned_id:
        for key, rec in by_title.items():
            if cleaned_id and (cleaned_id in key or key in cleaned_id):
                return rec
    return {}


def _to_int_year(v) -> Optional[int]:
    if v is None:
        return None
    s = str(v).strip()
    if not s:
        return None
    try:
        return int(s)
    except ValueError:
        m = re.search(r"(19|20)\d{2}", s)
        if m:
            try:
                return int(m.group(0))
            except ValueError:
                return None
    return None


# 核心判定：核心类型字段包含任一核心库标识即视为核心期刊文献
_CORE_MARKERS = ("北大核心", "CSSCI", "CSCD", "EI", "SCI")


def _derive_is_core(core_type: str) -> int:
    return 1 if any(m in core_type for m in _CORE_MARKERS) else 0


def _build_paper_record(
    paper_id: str,
    parsed: Dict[str, Any],
    meta: Dict[str, Any],
) -> Dict[str, Any]:
    title = (meta.get("title") or "").strip() or paper_id
    author = (meta.get("author") or "").strip()
    keywords = (meta.get("keywords") or "").strip()
    abstract = (meta.get("abstract") or "").strip()
    doi = (meta.get("doi") or "").strip()
    source_journal = (meta.get("source_journal") or "").strip()
    affiliation = (meta.get("affiliation") or "").strip()
    core_type = (meta.get("db_class") or "").strip()
    clc_number = (meta.get("subject_code") or "").strip()
    publish_year = _to_int_year(meta.get("publish_year"))
    research_design_text = parsed.get("research_design_text") or ""
    raw_body = parsed.get("full_text") or ""

    return {
        "paper_id": paper_id,
        "title": title,
        "doi": doi,
        "publish_year": publish_year,
        "author": author,
        "keywords": keywords,
        "abstract": abstract,
        "source_journal": source_journal,
        "affiliation": affiliation,
        "core_type": core_type,
        "is_core": _derive_is_core(core_type),
        "clc_number": clc_number,
        "research_design_text": research_design_text,
        "title_tokens": tokenize_join(title),
        "keywords_tokens": tokenize_join(keywords),
        "abstract_tokens": tokenize_join(abstract),
        "research_design_tokens": tokenize_join(research_design_text),
        "body_tokens": tokenize_join(raw_body),
        "raw_body": raw_body,
    }


def _process_one(
    docx_path: Path,
    csv_idx: Dict[str, Any],
    embedder,
    embed: bool,
    split_method: str = "semantic",
) -> Dict[str, Any]:
    paper_id = _derive_paper_id(docx_path)
    parsed = parse_docx(str(docx_path))
    title_hint = paper_id  # docx 文件名清洗后通常就是标题
    meta = _lookup_meta(csv_idx, paper_id, title_hint=title_hint)
    paper_record = _build_paper_record(paper_id, parsed, meta)

    chunks_meta: List[Dict[str, Any]] = chunk_paper(
        parsed, method=split_method, embedder=embedder if embed else None
    )
    chunk_records: List[Dict[str, Any]] = []

    # 批量计算 embedding
    if embed and embedder is not None and chunks_meta:
        texts = [c["chunk_text"] for c in chunks_meta]
        try:
            vectors = embedder.embed(texts)
        except Exception as e:
            print(f"[warn] embed failed for {paper_id}: {e}", file=sys.stderr)
            vectors = [None] * len(chunks_meta)
    else:
        vectors = [None] * len(chunks_meta)

    for i, ch in enumerate(chunks_meta):
        vec = vectors[i] if i < len(vectors) else None
        if vec is not None:
            try:
                emb_blob = vector_codec.encode(vec)
            except Exception:
                emb_blob = b""
        else:
            emb_blob = b""
        chunk_id = f"{paper_id}::{ch['chunk_index']}"
        chunk_records.append(
            {
                "chunk_id": chunk_id,
                "paper_id": paper_id,
                "chunk_index": int(ch["chunk_index"]),
                "paragraph_index": int(ch.get("paragraph_index", -1)),
                "offset_start": int(ch.get("offset_start", 0)),
                "chunk_text": ch["chunk_text"],
                "chapter_title": ch.get("chapter_title", ""),
                "chapter_index": int(ch.get("chapter_index", -1)),
                "section_role": ch.get("section_role", ""),
                "tag_confidence": ch.get("tag_confidence", ""),
                "split_method": ch.get("split_method", ""),
                "embedding": emb_blob if emb_blob else None,
            }
        )

    return {"paper": paper_record, "chunks": chunk_records}


def _iter_docx(data_dir: Path) -> List[Path]:
    word_dir = data_dir / "word"
    if not word_dir.exists():
        # 兜底：当前目录直接含 .docx
        word_dir = data_dir
    # 过滤 Word 临时锁文件（~$）与 macOS AppleDouble 伴随文件（._foo.docx）；
    # 后者在 macOS 上用 tar/scp 传输时常被一并带出，并非有效的 docx 包。
    files = sorted(
        p
        for p in word_dir.glob("*.docx")
        if not p.name.startswith("~$") and not p.name.startswith("._")
    )
    return files


def run(args: argparse.Namespace) -> int:
    data_dir = Path(args.data_dir).expanduser().resolve()
    csv_path = data_dir / "WanFangdata.csv"
    db_path = args.db or os.environ.get("DB_PATH") or DEFAULT_DB

    print(f"[ingest] data_dir = {data_dir}")
    print(f"[ingest] csv      = {csv_path}")
    print(f"[ingest] db       = {db_path}")

    # 1) 读 CSV
    df = None
    if csv_path.exists():
        from .csv_meta import load_metadata

        try:
            df = load_metadata(str(csv_path))
            print(f"[ingest] csv rows = {len(df)}")
        except Exception as e:
            print(f"[warn] load csv failed: {e}", file=sys.stderr)
            df = None
    else:
        print(f"[warn] csv not found at {csv_path}", file=sys.stderr)

    if df is None:
        import pandas as pd  # lazy

        df = pd.DataFrame(
            columns=[
                "serial", "title", "author", "keywords", "abstract", "doi",
                "cn", "affiliation", "source", "issn", "pages", "db_class",
                "subject_code", "source_journal", "publish_year",
            ]
        )
    csv_idx = _build_csv_indexes(df)

    # 2) 数据库
    conn = dbmod.connect(db_path)
    dbmod.ensure_schema(conn)

    # 3) embedder
    embedder = None
    if not args.no_embed:
        backend = args.embed_backend or os.environ.get("EMBED_BACKEND") or "local"
        model = args.embed_model or os.environ.get("EMBED_MODEL") or "BAAI/bge-small-zh-v1.5"
        api_key = os.environ.get("LLM_API_KEY")
        base_url = os.environ.get("LLM_BASE_URL")
        try:
            from .embeddings import Embedder

            embedder = Embedder(backend=backend, model=model, api_key=api_key, base_url=base_url)
            print(f"[ingest] embedder backend={backend} model={model}")
        except Exception as e:
            print(f"[warn] embedder init failed, fallback to --no-embed: {e}", file=sys.stderr)
            embedder = None
    else:
        print("[ingest] embedding disabled (--no-embed)")

    # 4) 遍历 docx
    files = _iter_docx(data_dir)
    if args.limit and args.limit > 0:
        files = files[: args.limit]
    total = len(files)
    print(f"[ingest] docx total = {total}")

    ok = 0
    failed = 0
    for i, path in enumerate(files, start=1):
        try:
            result = _process_one(
                path, csv_idx, embedder, embed=embedder is not None,
                split_method=args.split_method,
            )
            dbmod.upsert_paper(conn, result["paper"])
            # 切分方案变化会改变 chunk 数量，先清掉旧 chunk 防止残留
            conn.execute(
                "DELETE FROM paper_chunks WHERE paper_id = ?",
                (result["paper"]["paper_id"],),
            )
            for ch in result["chunks"]:
                dbmod.upsert_chunk(conn, ch)
            conn.commit()
            ok += 1
        except Exception as e:
            failed += 1
            print(f"[error] {path.name}: {e}", file=sys.stderr)
            traceback.print_exc(file=sys.stderr)
            try:
                conn.rollback()
            except Exception:
                pass
        if i % 50 == 0 or i == total:
            print(f"[ingest] progress {i}/{total} ok={ok} fail={failed}")

    n_papers = dbmod.count_papers(conn)
    n_chunks = dbmod.count_chunks(conn)
    print(f"[ingest] done. papers={n_papers} chunks={n_chunks}")
    conn.close()
    return 0 if failed == 0 else 2


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="python -m pyservice.ingest",
        description="Ingest WanFangdata.csv + word/*.docx into shared SQLite.",
    )
    p.add_argument("--data-dir", default=DEFAULT_DATA_DIR, help="数据根目录")
    p.add_argument("--limit", type=int, default=0, help="只处理前 N 篇（0=全部）")
    p.add_argument(
        "--db",
        default=None,
        help=f"SQLite 路径，默认环境变量 DB_PATH 或 {DEFAULT_DB}",
    )
    p.add_argument("--no-embed", action="store_true", help="跳过嵌入计算")
    p.add_argument("--embed-backend", default=None, help="local 或 openai")
    p.add_argument("--embed-model", default=None, help="嵌入模型名")
    p.add_argument(
        "--split-method",
        default="semantic",
        choices=["semantic", "greedy", "legacy"],
        help="切分方法：semantic=语义边界检测(默认) / greedy=段落贪心 / legacy=滑动窗口",
    )
    return p


def main(argv: Optional[List[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return run(args)


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
