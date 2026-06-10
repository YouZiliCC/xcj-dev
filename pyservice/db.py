"""SQLite 访问层，与 Go 后端共享同一份 schema/数据。"""

from __future__ import annotations

import os
import sqlite3
from pathlib import Path
from typing import Any, Dict, List, Optional


_SCHEMA_PATH = (
    Path(__file__).resolve().parent.parent
    / "backend"
    / "migrations"
    / "001_init.sql"
)


def connect(path: str) -> sqlite3.Connection:
    """打开 SQLite 连接，启用 WAL 与外键。"""
    parent = os.path.dirname(os.path.abspath(path))
    if parent:
        os.makedirs(parent, exist_ok=True)
    conn = sqlite3.connect(path)
    conn.row_factory = sqlite3.Row
    try:
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute("PRAGMA foreign_keys=ON")
        conn.execute("PRAGMA synchronous=NORMAL")
    except sqlite3.DatabaseError:
        pass
    return conn


def ensure_schema(conn: sqlite3.Connection, schema_path: Optional[str] = None) -> None:
    """执行 backend/migrations/001_init.sql 全文（幂等），并补齐增量列。"""
    path = Path(schema_path) if schema_path else _SCHEMA_PATH
    sql_text = path.read_text(encoding="utf-8")
    conn.executescript(sql_text)
    _ensure_columns(conn)
    conn.commit()


def _ensure_columns(conn: sqlite3.Connection) -> None:
    """对已存在的库幂等补齐新增列（CREATE TABLE IF NOT EXISTS 不会改动旧表）。

    与 backend/migrations/002_affiliation.sql、003_filters_chunkmeta.sql 保持一致。
    """
    existing = {row[1] for row in conn.execute("PRAGMA table_info(papers_master)").fetchall()}
    wanted = {
        "affiliation": "TEXT",
        "core_type": "TEXT",
        "is_core": "INTEGER",
        "clc_number": "TEXT",
    }
    for col, col_type in wanted.items():
        if col not in existing:
            conn.execute(f"ALTER TABLE papers_master ADD COLUMN {col} {col_type}")

    existing_chunks = {row[1] for row in conn.execute("PRAGMA table_info(paper_chunks)").fetchall()}
    wanted_chunks = {
        "chapter_title": "TEXT",
        "chapter_index": "INTEGER",
        "section_role": "TEXT",
        "tag_confidence": "TEXT",
        "split_method": "TEXT",
    }
    for col, col_type in wanted_chunks.items():
        if col not in existing_chunks:
            conn.execute(f"ALTER TABLE paper_chunks ADD COLUMN {col} {col_type}")


_PAPER_COLUMNS = (
    "paper_id",
    "title",
    "doi",
    "publish_year",
    "author",
    "keywords",
    "abstract",
    "source_journal",
    "affiliation",
    "core_type",
    "is_core",
    "clc_number",
    "research_design_text",
    "title_tokens",
    "keywords_tokens",
    "abstract_tokens",
    "research_design_tokens",
    "body_tokens",
    "raw_body",
)

_CHUNK_COLUMNS = (
    "chunk_id",
    "paper_id",
    "chunk_index",
    "paragraph_index",
    "offset_start",
    "chunk_text",
    "chapter_title",
    "chapter_index",
    "section_role",
    "tag_confidence",
    "split_method",
    "embedding",
)


def upsert_paper(conn: sqlite3.Connection, paper: Dict[str, Any]) -> None:
    values = [paper.get(c) for c in _PAPER_COLUMNS]
    placeholders = ",".join(["?"] * len(_PAPER_COLUMNS))
    update_clause = ",\n  ".join(
        f"{c}=excluded.{c}" for c in _PAPER_COLUMNS if c != "paper_id"
    )
    sql = (
        f"INSERT INTO papers_master({','.join(_PAPER_COLUMNS)}) "
        f"VALUES ({placeholders}) "
        f"ON CONFLICT(paper_id) DO UPDATE SET\n  {update_clause}"
    )
    conn.execute(sql, values)


def upsert_chunk(conn: sqlite3.Connection, chunk: Dict[str, Any]) -> None:
    values = [chunk.get(c) for c in _CHUNK_COLUMNS]
    placeholders = ",".join(["?"] * len(_CHUNK_COLUMNS))
    update_clause = ",\n  ".join(
        f"{c}=excluded.{c}" for c in _CHUNK_COLUMNS if c != "chunk_id"
    )
    sql = (
        f"INSERT INTO paper_chunks({','.join(_CHUNK_COLUMNS)}) "
        f"VALUES ({placeholders}) "
        f"ON CONFLICT(chunk_id) DO UPDATE SET\n  {update_clause}"
    )
    conn.execute(sql, values)


def count_papers(conn: sqlite3.Connection) -> int:
    row = conn.execute("SELECT COUNT(*) FROM papers_master").fetchone()
    return int(row[0]) if row else 0


def count_chunks(conn: sqlite3.Connection) -> int:
    row = conn.execute("SELECT COUNT(*) FROM paper_chunks").fetchone()
    return int(row[0]) if row else 0


def all_papers(conn: sqlite3.Connection) -> List[Dict[str, Any]]:
    cursor = conn.execute(
        f"SELECT {','.join(_PAPER_COLUMNS)} FROM papers_master"
    )
    out: List[Dict[str, Any]] = []
    for row in cursor.fetchall():
        out.append({col: row[col] for col in _PAPER_COLUMNS})
    return out


__all__ = [
    "connect",
    "ensure_schema",
    "upsert_paper",
    "upsert_chunk",
    "count_papers",
    "count_chunks",
    "all_papers",
]
