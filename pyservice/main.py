"""FastAPI 应用：嵌入、查询重写、生成、数据分析。

启动：
    uvicorn pyservice.main:app --host 0.0.0.0 --port 8001 --reload
"""

from __future__ import annotations

import json
import os
import re
import threading
import traceback
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, StreamingResponse
from pydantic import BaseModel, Field

from . import analyze as analyze_mod
from . import db as dbmod
from . import llm as llm_mod
from .embeddings import Embedder
from .tokenize_cn import tokenize

DEFAULT_DB = "/Users/deeryou/xcj-dev/data/storage/papers.db"

app = FastAPI(title="xcj-dev pyservice", version="0.1.0")


# --- lazy resources ---

_embedder_lock = threading.Lock()
_embedder: Optional[Embedder] = None


def _get_embedder() -> Embedder:
    global _embedder
    if _embedder is not None:
        return _embedder
    with _embedder_lock:
        if _embedder is None:
            backend = os.environ.get("EMBED_BACKEND") or "local"
            model = os.environ.get("EMBED_MODEL") or "BAAI/bge-small-zh-v1.5"
            api_key = os.environ.get("LLM_API_KEY")
            base_url = os.environ.get("LLM_BASE_URL")
            _embedder = Embedder(
                backend=backend, model=model, api_key=api_key, base_url=base_url
            )
        return _embedder


def _get_conn():
    db_path = os.environ.get("DB_PATH") or DEFAULT_DB
    conn = dbmod.connect(db_path)
    return conn


# --- schemas ---


class EmbedRequest(BaseModel):
    texts: List[str] = Field(default_factory=list)


class EmbedResponse(BaseModel):
    vectors: List[List[float]]


class RewriteRequest(BaseModel):
    q: str


class GenChunk(BaseModel):
    """带全局引用编号的取证文本块（QA 五段式细粒度引用）。"""

    ref_id: int = 0
    chunk_id: str = ""
    chunk_index: int = 0
    text: str = ""
    section_role: str = ""
    chapter_title: str = ""
    score: float = 0.0


class GeneratePaper(BaseModel):
    paper_id: str = ""
    title: str = ""
    doi: str = ""
    author: str = ""
    keywords: str = ""
    abstract: str = ""
    publish_year: Optional[int] = None
    journal: str = ""
    research_design_text: str = ""
    top_chunk_text: str = ""
    relevance_score: float = 0.0
    chunks: List[GenChunk] = Field(default_factory=list)


class GenerateRequest(BaseModel):
    query: str
    papers: List[GeneratePaper] = Field(default_factory=list)


class AnalyzeRequest(BaseModel):
    kind: str
    params: Dict[str, Any] = Field(default_factory=dict)


# --- routes ---


@app.get("/health")
def health() -> Dict[str, Any]:
    dim = 0
    try:
        # 不强制初始化模型；如果还没加载就返回 0
        if _embedder is not None:
            dim = _embedder.dim
    except Exception:
        dim = 0
    return {"status": "ok", "dim": int(dim)}


@app.post("/embed", response_model=EmbedResponse)
def embed(req: EmbedRequest) -> EmbedResponse:
    try:
        embedder = _get_embedder()
        vectors = embedder.embed(req.texts or [])
        return EmbedResponse(vectors=vectors)
    except HTTPException:
        raise
    except Exception as e:
        traceback.print_exc()
        raise HTTPException(status_code=500, detail={"error": "embed_failed", "message": str(e)})


# --- rewrite ---

_REWRITE_SYSTEM = (
    "你是学术检索查询重写助手。请严格按 JSON Schema 输出，不要解释，"
    "不要使用 markdown 代码块。\n"
    "返回结构：\n"
    "{\n"
    '  "filter_conditions": {\n'
    '    "publish_year": 整数或 null,\n'
    '    "year_from": 整数或 null,\n'
    '    "year_to": 整数或 null,\n'
    '    "authors": [字符串...],\n'
    '    "affiliation": [字符串...],\n'
    '    "journal": [字符串...],\n'
    '    "is_core": true/false/null,\n'
    '    "clc_number": [字符串...]\n'
    "  },\n"
    '  "boolean_query": 字符串,\n'
    '  "search_payload": {\n'
    '    "core_semantic_sentence": 字符串,\n'
    '    "academic_keywords": [字符串...],\n'
    '    "synonyms_and_extensions": [字符串...],\n'
    '    "potential_variables": [字符串...],\n'
    '    "research_design_terms": [字符串...]\n'
    "  }\n"
    "}\n"
    "规则：\n"
    "- filter_conditions 只在用户问题中【显式提到】对应条件时才填：提到作者填 authors、"
    "提到单位/学校填 affiliation、提到期刊填 journal、明确要求核心期刊填 is_core=true、"
    "提到中图分类号填 clc_number、提到年份填 publish_year（单年）或 year_from/year_to（区间）。"
    "未提到的字段一律为 null 或 []，严禁臆测。\n"
    "- boolean_query：把核心检索词组织成布尔检索式，同义词/近义词用 OR 并用括号分组，"
    '不同概念之间用 AND，需要排除的主题用 NOT，例如 "(深度学习 OR 机器学习) AND (图像分类 OR 目标检测) NOT 综述"。'
    "没有排除需求就不要用 NOT。\n"
    "- 如果用户没指定年份则 publish_year 为 null。\n"
    "- 如果没有明确的方法/模型/算法/实验设计意图，research_design_terms 必须为 []。\n"
    "- 关键词列表不要包含停用词，去重，使用中文学术词。"
)


_YEAR_RE = re.compile(r"(19|20)\d{2}")


def _fallback_rewrite(q: str) -> Dict[str, Any]:
    """LLM 失败时的启发式回退：直接分词 + 年份正则。"""
    year_match = _YEAR_RE.search(q or "")
    year: Optional[int] = None
    if year_match:
        try:
            year = int(year_match.group(0))
        except ValueError:
            year = None
    # 简易关键词：取 token 中长度 >= 2 的中文 bigram + ASCII 词
    raw_tokens = tokenize(q or "")
    keywords: List[str] = []
    seen = set()
    for t in raw_tokens:
        if t.isascii():
            if len(t) >= 2 and t not in seen:
                seen.add(t)
                keywords.append(t)
        else:
            if len(t) >= 2 and t not in seen:
                seen.add(t)
                keywords.append(t)
    return {
        "filter_conditions": {"publish_year": year},
        "boolean_query": "",
        "search_payload": {
            "core_semantic_sentence": q or "",
            "academic_keywords": keywords[:12],
            "synonyms_and_extensions": [],
            "potential_variables": [],
            "research_design_terms": [],
        },
    }


def _strip_codefence(text: str) -> str:
    if not text:
        return ""
    s = text.strip()
    if s.startswith("```"):
        # 去掉 ```json ... ``` 包裹
        s = re.sub(r"^```[a-zA-Z]*\s*", "", s)
        s = re.sub(r"\s*```\s*$", "", s)
    return s.strip()


def _normalize_rewrite(raw: Dict[str, Any], q: str) -> Dict[str, Any]:
    base = _fallback_rewrite(q)
    if not isinstance(raw, dict):
        return base
    filt = raw.get("filter_conditions") or {}
    if not isinstance(filt, dict):
        filt = {}
    publish_year = filt.get("publish_year")
    if isinstance(publish_year, str):
        m = _YEAR_RE.search(publish_year)
        publish_year = int(m.group(0)) if m else None
    elif isinstance(publish_year, (int, float)):
        publish_year = int(publish_year)
    else:
        publish_year = None

    payload = raw.get("search_payload") or {}
    if not isinstance(payload, dict):
        payload = {}

    def _as_list(v) -> List[str]:
        if v is None:
            return []
        if isinstance(v, str):
            return [v] if v.strip() else []
        if isinstance(v, list):
            return [str(x).strip() for x in v if str(x).strip()]
        return []

    def _as_int(v) -> Optional[int]:
        if isinstance(v, (int, float)):
            return int(v)
        if isinstance(v, str):
            m = _YEAR_RE.search(v)
            return int(m.group(0)) if m else None
        return None

    conditions: Dict[str, Any] = {
        "publish_year": publish_year,
        "year_from": _as_int(filt.get("year_from")),
        "year_to": _as_int(filt.get("year_to")),
        "authors": _as_list(filt.get("authors") or filt.get("author")),
        "affiliation": _as_list(filt.get("affiliation")),
        "journal": _as_list(filt.get("journal") or filt.get("source_journal")),
        "is_core": filt.get("is_core") if isinstance(filt.get("is_core"), bool) else None,
        "clc_number": _as_list(filt.get("clc_number") or filt.get("clc")),
    }

    return {
        "filter_conditions": conditions,
        "boolean_query": str(raw.get("boolean_query") or "").strip(),
        "search_payload": {
            "core_semantic_sentence": str(payload.get("core_semantic_sentence") or q or "").strip(),
            "academic_keywords": _as_list(payload.get("academic_keywords"))
            or base["search_payload"]["academic_keywords"],
            "synonyms_and_extensions": _as_list(payload.get("synonyms_and_extensions")),
            "potential_variables": _as_list(payload.get("potential_variables")),
            "research_design_terms": _as_list(payload.get("research_design_terms")),
        },
    }


@app.post("/rewrite")
def rewrite(req: RewriteRequest) -> Dict[str, Any]:
    q = (req.q or "").strip()
    if not q:
        return _fallback_rewrite("")
    model = os.environ.get("LLM_MODEL") or ""
    base_url = os.environ.get("LLM_BASE_URL")
    api_key = os.environ.get("LLM_API_KEY")
    try:
        temperature = float(os.environ.get("LLM_TEMPERATURE") or 0.2)
    except ValueError:
        temperature = 0.2

    if not model or not api_key:
        return _fallback_rewrite(q)

    messages = [
        {"role": "system", "content": _REWRITE_SYSTEM},
        {"role": "user", "content": q},
    ]
    try:
        content = llm_mod.chat(
            messages,
            model=model,
            temperature=temperature,
            base_url=base_url,
            api_key=api_key,
            response_format={"type": "json_object"},
        )
        content = _strip_codefence(content)
        if not content:
            return _fallback_rewrite(q)
        try:
            data = json.loads(content)
        except json.JSONDecodeError:
            # 再尝试取首个 JSON 对象
            m = re.search(r"\{[\s\S]*\}", content)
            if not m:
                return _fallback_rewrite(q)
            try:
                data = json.loads(m.group(0))
            except json.JSONDecodeError:
                return _fallback_rewrite(q)
        return _normalize_rewrite(data, q)
    except Exception:
        traceback.print_exc()
        return _fallback_rewrite(q)


# --- generate ---


_GENERATE_SYSTEM = (
    "你是学术文献综述撰写助手，基于提供的核心文献撰写一篇结构完整、跨学科通用的文献综述。\n"
    "硬性规则：\n"
    "规则 1（直接出正文）：直接输出综述正文本身。严禁任何开场白、寒暄或自我说明——"
    "诸如「好的」「作为严谨的学术研究助理」「以下是」「我将」「根据您提供的文献」等都不得出现；"
    "也严禁过程性旁白，诸如「以下分条阐述…」「接下来分析…」「综上所述，本综述将…」。\n"
    "规则 2（学术溯源）：每一处引述、数据或结论句末必须标注来源锚点 [DOI:xxx]；"
    "若文献无 DOI 则用 [PaperID:xxx]。\n"
    "规则 3（零幻觉红线）：只能基于提供的文献资料，资料中没有的内容必须明说「现有文献未涉及」，严禁编造。\n"
    "规则 4（通用结构）：用 Markdown 小标题组织（不要用代码块），采用以下跨学科通用框架，"
    "不要强行套用「自变量/因变量/中介」等特定学科范式：\n"
    "## 研究背景与综述范围\n"
    "## 研究现状与主要观点（按主题、视角或流派归纳，而非逐篇罗列）\n"
    "## 研究方法与数据（不同文献的方法、样本与数据来源的分布与异同）\n"
    "## 争议、分歧与研究空白\n"
    "## 趋势与未来展望\n"
    "规则 5（语言风格）：简体中文，客观、概括、以归纳综合为主。"
)


# 模型偶尔仍会带出的开场白/寒暄标志词，作为 prompt 之外的防御性兜底。
_PREAMBLE_MARKERS = (
    "好的", "好的，", "作为", "以下是", "以下，", "我将", "我会",
    "根据您", "根据你", "首先，我", "明白", "收到",
)


def _strip_preamble(text: str) -> str:
    """剥离模型可能残留的开场白首段（保守：只删较短的、命中标志词的首行）。"""
    s = (text or "").lstrip()
    if not s:
        return text or ""
    nl = s.find("\n")
    first_line = s if nl < 0 else s[:nl]
    stripped = first_line.lstrip()
    if len(stripped) <= 50 and any(stripped.startswith(m) for m in _PREAMBLE_MARKERS):
        rest = s[nl + 1:].lstrip() if nl >= 0 else ""
        if rest:
            return rest
    return s


def _clean_stream(pieces):
    """对流式增量做一次性开场白剥离：缓冲到首个换行或 80 字再统一处理。"""
    buf = ""
    started = False
    for p in pieces:
        if started:
            yield p
            continue
        buf += p
        if "\n" in buf or len(buf) >= 80:
            cleaned = _strip_preamble(buf)
            started = True
            if cleaned:
                yield cleaned
            buf = ""
    if not started:
        cleaned = _strip_preamble(buf)
        if cleaned:
            yield cleaned


def _build_context_stream(papers: List[GeneratePaper]) -> str:
    parts: List[str] = []
    for idx, p in enumerate(papers, start=1):
        doi = p.doi.strip() or f"PaperID:{p.paper_id}"
        title = p.title.strip() or p.paper_id
        year = p.publish_year if p.publish_year is not None else "未知"
        author = p.author.strip() or "（未提供）"
        keywords = p.keywords.strip() or "（未提供）"
        abstract = p.abstract.strip() or "（未提供）"
        chunk = p.top_chunk_text.strip() or "（未提供）"
        design = p.research_design_text.strip() or "（未提供）"
        block = (
            f"【核心文献 {idx}】\n"
            f"标题: {title} | DOI: {doi} | 发表年份: {year}\n"
            f"作者: {author}\n"
            f"关键词: {keywords}\n"
            f"摘要: {abstract}\n"
            f"相关语义片段: {chunk}\n"
            f"独立研究设计原文: {design}\n"
        )
        parts.append(block)
    return "\n\n".join(parts)


def _generate_messages(query: str, papers: List[GeneratePaper]) -> List[Dict[str, str]]:
    context_stream = _build_context_stream(papers)
    user_prompt = (
        f"用户研究问题（综述主题）：{query}\n\n"
        f"以下是检索系统返回的 Top {len(papers)} 篇核心文献，按相关性从高到低排序：\n\n"
        f"{context_stream}\n\n"
        "请基于以上文献撰写文献综述正文（直接从第一个小标题开始，不要任何开场白）。"
    )
    return [
        {"role": "system", "content": _GENERATE_SYSTEM},
        {"role": "user", "content": user_prompt},
    ]


@app.post("/generate")
def generate(req: GenerateRequest) -> Dict[str, Any]:
    query = (req.query or "").strip()
    papers = req.papers or []
    if not query:
        raise HTTPException(status_code=400, detail={"error": "empty_query"})

    model = os.environ.get("LLM_MODEL") or ""
    base_url = os.environ.get("LLM_BASE_URL")
    api_key = os.environ.get("LLM_API_KEY")
    if not model or not api_key:
        raise HTTPException(
            status_code=500,
            detail={"error": "llm_not_configured", "message": "LLM_MODEL / LLM_API_KEY missing"},
        )
    try:
        temperature = float(os.environ.get("LLM_TEMPERATURE") or 0.2)
    except ValueError:
        temperature = 0.2

    messages = _generate_messages(query, papers)

    try:
        answer = llm_mod.chat(
            messages,
            model=model,
            temperature=temperature,
            base_url=base_url,
            api_key=api_key,
        )
    except Exception as e:
        traceback.print_exc()
        raise HTTPException(
            status_code=500,
            detail={"error": "generate_failed", "message": str(e)},
        )
    answer = _strip_preamble(answer or "")

    citations: List[Dict[str, Any]] = []
    for p in papers:
        citations.append(
            {
                "paper_id": p.paper_id,
                "title": p.title,
                "doi": p.doi,
                "author": p.author,
                "keywords": p.keywords,
                "abstract": p.abstract,
                "publish_year": p.publish_year,
                "relevance_score": p.relevance_score,
                "chunk_text": p.top_chunk_text,
            }
        )

    return {"answer": answer or "", "citations": citations}


# --- shared llm config ---


def _llm_config() -> Dict[str, Any]:
    """与 /generate 一致的 env 读取方式。"""
    model = os.environ.get("LLM_MODEL") or ""
    base_url = os.environ.get("LLM_BASE_URL")
    api_key = os.environ.get("LLM_API_KEY")
    try:
        temperature = float(os.environ.get("LLM_TEMPERATURE") or 0.2)
    except ValueError:
        temperature = 0.2
    return {
        "model": model,
        "base_url": base_url,
        "api_key": api_key,
        "temperature": temperature,
    }


def _require_llm(cfg: Dict[str, Any]) -> None:
    if not cfg["model"] or not cfg["api_key"]:
        raise HTTPException(
            status_code=500,
            detail={"error": "llm_not_configured", "message": "LLM_MODEL / LLM_API_KEY missing"},
        )


def _call_chat(messages: List[Dict[str, str]], cfg: Dict[str, Any], error: str,
               response_format: Optional[Dict[str, Any]] = None) -> str:
    try:
        return llm_mod.chat(
            messages,
            model=cfg["model"],
            temperature=cfg["temperature"],
            base_url=cfg["base_url"],
            api_key=cfg["api_key"],
            response_format=response_format,
        )
    except Exception as e:
        traceback.print_exc()
        raise HTTPException(status_code=500, detail={"error": error, "message": str(e)})


def _ndjson_stream(messages: List[Dict[str, str]], cfg: Dict[str, Any],
                   clean_preamble: bool = True) -> StreamingResponse:
    """以 NDJSON 流式返回 LLM 增量：每行一个 JSON——

    {"delta": "..."} 文本增量 / {"done": true} 结束 / {"error": "..."} 出错。
    Go 后端逐行读取并重新封装为 SSE 发给浏览器。
    """

    def gen():
        try:
            pieces = llm_mod.chat_stream(
                messages,
                model=cfg["model"],
                temperature=cfg["temperature"],
                base_url=cfg["base_url"],
                api_key=cfg["api_key"],
            )
            if clean_preamble:
                pieces = _clean_stream(pieces)
            for piece in pieces:
                yield json.dumps({"delta": piece}, ensure_ascii=False) + "\n"
            yield json.dumps({"done": True}) + "\n"
        except Exception as e:  # noqa: BLE001
            traceback.print_exc()
            yield json.dumps({"error": str(e)}, ensure_ascii=False) + "\n"

    return StreamingResponse(gen(), media_type="application/x-ndjson")


# --- qa (T4 智能问答) ---


_QA_SYSTEM = (
    "你是学术研究问答助手，只能基于下方提供的带编号文献证据回答用户问题。\n"
    "必须严格按以下五段固定结构输出（Markdown 二级标题，顺序不得调换、不得缺失任何一段）：\n"
    "## 概念解释\n"
    "（定义问题涉及的核心概念、指标或理论，约 80-150 字）\n"
    "## 背景说明\n"
    "（说明问题背景与研究动因，约 80-150 字）\n"
    "## 方法依据\n"
    "（说明相关方法、模型、算法或实验设计，约 80-200 字）\n"
    "## 经验证据\n"
    "（给出数据、实验或案例结果，约 80-200 字）\n"
    "## 研究空白\n"
    "（指出现有研究不足与未来方向，约 60-120 字）\n"
    "硬性规则：\n"
    "1. 只能依据提供的【证据n】作答，严禁编造证据之外的事实。\n"
    "2. 段内每个关键论断后用方括号编号标注来源证据，如 [1] 或 [1][3]；编号必须与证据编号一一对应。\n"
    "3. 每段末尾另起一行写「参考文献：[n][m]」列出该段引用的全部证据编号。\n"
    "4. 某段若没有相关证据支撑，该段正文只写「暂无相关文献证据」，不写参考文献行，严禁硬凑。\n"
    "5. 每条证据标注了段落作用（概念解释/背景说明/方法依据/经验证据/研究空白），"
    "优先把对应作用的证据用于对应段落，内容相关时也可跨段引用。\n"
    "6. 直接从「## 概念解释」开始输出，不要任何开场白；禁止使用代码块。\n"
    "输出语言：简体中文。"
)


class QaRequest(BaseModel):
    question: str = ""
    papers: List[GeneratePaper] = Field(default_factory=list)
    evidence_sufficient: bool = True


def _build_qa_context(papers: List[GeneratePaper]) -> str:
    """组装带全局编号的证据块。优先用细粒度 chunks（含 ref_id/段落作用）；
    旧调用方未传 chunks 时回退为每篇一条证据。"""
    parts: List[str] = []
    has_chunks = any(p.chunks for p in papers)
    if has_chunks:
        for p in papers:
            doi = p.doi.strip() or f"PaperID:{p.paper_id}"
            title = p.title.strip() or p.paper_id
            year = p.publish_year if p.publish_year is not None else "未知"
            for c in p.chunks:
                role = c.section_role or "未标注"
                chapter = c.chapter_title or "未知章节"
                parts.append(
                    f"【证据{c.ref_id}】（段落作用: {role} | 章节: {chapter}）{title}|{doi}|{year}\n"
                    f"{c.text.strip()}"
                )
        return "\n\n".join(parts)
    for idx, p in enumerate(papers, start=1):
        doi = p.doi.strip() or f"PaperID:{p.paper_id}"
        title = p.title.strip() or p.paper_id
        year = p.publish_year if p.publish_year is not None else "未知"
        chunk = p.top_chunk_text.strip() or "（未提供）"
        design = p.research_design_text.strip()
        if len(design) > 800:
            design = design[:800] + "…"
        design = design or "（未提供）"
        block = (
            f"【证据{idx}】{title}|{doi}|{year}\n"
            f"摘要片段: {chunk}\n"
            f"研究设计: {design}"
        )
        parts.append(block)
    return "\n\n".join(parts)


def _qa_messages(question: str, papers: List[GeneratePaper],
                 evidence_sufficient: bool) -> List[Dict[str, str]]:
    context = _build_qa_context(papers)
    prefix = ""
    if not evidence_sufficient:
        prefix = "请注意：检索到的相关文献较少，请在「## 概念解释」之前先单独输出一行“（注：检索到的相关文献较少，以下结论可信度有限）”。\n\n"
    user_prompt = (
        f"{prefix}"
        f"用户问题：{question}\n\n"
        f"以下是检索到的带编号文献证据：\n\n"
        f"{context}\n\n"
        "请严格按系统要求的五段结构、基于以上证据回答用户问题。"
    )
    return [
        {"role": "system", "content": _QA_SYSTEM},
        {"role": "user", "content": user_prompt},
    ]


@app.post("/qa")
def qa(req: QaRequest) -> Dict[str, Any]:
    question = (req.question or "").strip()
    if not question:
        raise HTTPException(status_code=400, detail={"error": "empty_question"})
    cfg = _llm_config()
    _require_llm(cfg)

    messages = _qa_messages(question, req.papers or [], req.evidence_sufficient)
    answer = _call_chat(messages, cfg, "qa_failed")
    return {"answer": _strip_preamble(answer or "")}


@app.post("/generate_stream")
def generate_stream(req: GenerateRequest) -> StreamingResponse:
    query = (req.query or "").strip()
    if not query:
        raise HTTPException(status_code=400, detail={"error": "empty_query"})
    cfg = _llm_config()
    _require_llm(cfg)
    return _ndjson_stream(_generate_messages(query, req.papers or []), cfg)


@app.post("/qa_stream")
def qa_stream(req: QaRequest) -> StreamingResponse:
    question = (req.question or "").strip()
    if not question:
        raise HTTPException(status_code=400, detail={"error": "empty_question"})
    cfg = _llm_config()
    _require_llm(cfg)
    return _ndjson_stream(_qa_messages(question, req.papers or [], req.evidence_sufficient), cfg)


# --- summary (T5 ai 概要) ---


_SUMMARY_SYSTEM = (
    "你是学术论文结构化提炼助手。请基于提供的论文全文，提炼出概要、方法、结果与关键词。\n"
    "只能依据论文内容，严禁编造。\n"
    "请严格输出 JSON 对象，不要解释、不要使用 markdown 代码块：\n"
    "{\n"
    '  "summary": "论文概要（中文，简洁完整）",\n'
    '  "method": "研究方法",\n'
    '  "result": "主要结果",\n'
    '  "keywords": ["5到10个中文关键词"]\n'
    "}"
)


class PaperPayload(BaseModel):
    paper_id: str = ""
    title: str = ""
    author: str = ""
    doi: str = ""
    publish_year: Optional[int] = None
    journal: str = ""
    keywords: str = ""
    abstract: str = ""
    research_design_text: str = ""
    full_text: str = ""


class SummaryRequest(BaseModel):
    paper: PaperPayload = Field(default_factory=PaperPayload)


class ChatPaperRequest(BaseModel):
    paper: PaperPayload = Field(default_factory=PaperPayload)
    question: str = ""


def _paper_header(p: PaperPayload) -> str:
    year = p.publish_year if p.publish_year is not None else "未知"
    return (
        f"标题: {p.title.strip() or p.paper_id}\n"
        f"作者: {p.author.strip() or '（未提供）'}\n"
        f"DOI: {p.doi.strip() or '（未提供）'} | 期刊: {p.journal.strip() or '（未提供）'} | 年份: {year}\n"
        f"关键词: {p.keywords.strip() or '（未提供）'}\n"
        f"摘要: {p.abstract.strip() or '（未提供）'}"
    )


def _split_keywords(text: str) -> List[str]:
    if not text:
        return []
    parts = re.split(r"[,，;；、\s]+", text.strip())
    out: List[str] = []
    seen = set()
    for x in parts:
        x = x.strip()
        if x and x not in seen:
            seen.add(x)
            out.append(x)
    return out


@app.post("/summary")
def summary(req: SummaryRequest) -> Dict[str, Any]:
    p = req.paper
    cfg = _llm_config()
    _require_llm(cfg)

    full_text = (p.full_text or "").strip()
    if len(full_text) > 6000:
        full_text = full_text[:6000] + "…"
    body = full_text or p.abstract.strip() or "（未提供全文）"
    user_prompt = (
        f"{_paper_header(p)}\n\n"
        f"论文全文：\n{body}\n\n"
        "请按系统要求输出结构化 JSON。"
    )
    messages = [
        {"role": "system", "content": _SUMMARY_SYSTEM},
        {"role": "user", "content": user_prompt},
    ]
    content = _call_chat(messages, cfg, "summary_failed", response_format={"type": "json_object"})

    fallback = {
        "summary": p.abstract.strip(),
        "method": "",
        "result": "",
        "keywords": _split_keywords(p.keywords),
    }
    content = _strip_codefence(content or "")
    data: Optional[Dict[str, Any]] = None
    if content:
        try:
            data = json.loads(content)
        except json.JSONDecodeError:
            m = re.search(r"\{[\s\S]*\}", content)
            if m:
                try:
                    data = json.loads(m.group(0))
                except json.JSONDecodeError:
                    data = None
    if not isinstance(data, dict):
        return fallback

    kws = data.get("keywords")
    if isinstance(kws, str):
        kws = _split_keywords(kws)
    elif isinstance(kws, list):
        kws = [str(x).strip() for x in kws if str(x).strip()]
    else:
        kws = []
    if not kws:
        kws = fallback["keywords"]

    return {
        "summary": str(data.get("summary") or fallback["summary"]).strip(),
        "method": str(data.get("method") or "").strip(),
        "result": str(data.get("result") or "").strip(),
        "keywords": kws,
    }


# --- mindmap (T5 思维导图) ---


_MINDMAP_SYSTEM = (
    "你是学术论文思维导图生成助手。请基于提供的论文，输出一段 Markdown 大纲，"
    "供前端 markmap 渲染成可展开/收起的真·思维导图。\n"
    "硬性要求：\n"
    "1. 只输出 Markdown 大纲本身，不要任何解释文字，不要使用 ``` 代码围栏。\n"
    "2. 第一行是一级标题作为根节点：`# 论文主题`（取论文标题或其核心主题）。\n"
    "3. 用二级标题 `## ` 作为主分支，建议包含：研究背景、研究方法、数据、主要发现、结论、关键词。\n"
    "4. 每个分支下用 `- ` 列出 2-5 个要点，必要时用缩进的 `  - ` 表示子要点。\n"
    "5. 节点文本简洁（约 20 字以内），不要在节点里塞整段话。"
)


def _normalize_markmap(text: str, title: str) -> str:
    """清洗模型输出为合法 markmap 大纲：去围栏、确保单一一级标题根节点。"""
    s = (text or "").strip()
    if s.startswith("```"):
        s = re.sub(r"^```[a-zA-Z]*\s*", "", s)
        s = re.sub(r"\s*```\s*$", "", s)
    s = s.replace("```", "").strip()
    # 去掉可能残留的 mermaid/mindmap 关键字行
    lines = [ln for ln in s.splitlines() if ln.strip().lower() not in ("mermaid", "mindmap")]
    s = "\n".join(lines).strip()
    if not s:
        return f"# {title}"
    first = s.splitlines()[0].strip()
    if not re.match(r"^#\s+\S", first):
        s = f"# {title}\n\n{s}"
    return s


@app.post("/mindmap")
def mindmap(req: SummaryRequest) -> Dict[str, Any]:
    p = req.paper
    cfg = _llm_config()
    _require_llm(cfg)

    full_text = (p.full_text or "").strip()
    if len(full_text) > 6000:
        full_text = full_text[:6000] + "…"
    body = full_text or p.abstract.strip() or "（未提供全文）"
    user_prompt = (
        f"{_paper_header(p)}\n\n"
        f"论文全文：\n{body}\n\n"
        "请输出该论文的 Markdown 大纲（供 markmap 渲染思维导图）。"
    )
    messages = [
        {"role": "system", "content": _MINDMAP_SYSTEM},
        {"role": "user", "content": user_prompt},
    ]
    content = _call_chat(messages, cfg, "mindmap_failed")
    title = p.title.strip() or p.paper_id or "论文"
    markdown = _normalize_markmap(content or "", title)
    return {"markdown": markdown}


# --- chat (T5 ai 同读) ---


_CHAT_SYSTEM = (
    "你是针对单篇论文的阅读助手。\n"
    "硬性规则：\n"
    "1. 只能基于提供的这篇论文内容（标题/摘要/研究设计/全文）回答用户问题。\n"
    "2. 论文未涉及的内容，必须回答“该论文未提供相关信息”，严禁编造。\n"
    "3. 可以引用论文原文片段以支撑回答。\n"
    "输出语言：简体中文。"
)


def _pick_snippets(full_text: str, question: str, max_n: int = 3) -> List[str]:
    text = (full_text or "").strip()
    q = (question or "").strip()
    if not text or not q:
        return []
    paras = [s.strip() for s in re.split(r"\n{2,}|\n", text) if s.strip()]
    terms = [t for t in _split_keywords(q) if len(t) >= 2]
    if not terms:
        return []
    snippets: List[str] = []
    for para in paras:
        if any(t in para for t in terms):
            snip = para if len(para) <= 300 else para[:300] + "…"
            snippets.append(snip)
            if len(snippets) >= max_n:
                break
    return snippets


def _chat_messages(p: PaperPayload, question: str) -> List[Dict[str, str]]:
    full_text = (p.full_text or "").strip()
    if len(full_text) > 8000:
        full_text = full_text[:8000] + "…"
    design = p.research_design_text.strip() or "（未提供）"
    body = full_text or "（未提供全文）"
    user_prompt = (
        f"{_paper_header(p)}\n\n"
        f"研究设计原文: {design}\n\n"
        f"论文全文：\n{body}\n\n"
        f"用户问题：{question}\n\n"
        "请仅基于这篇论文作答。"
    )
    return [
        {"role": "system", "content": _CHAT_SYSTEM},
        {"role": "user", "content": user_prompt},
    ]


@app.post("/chat")
def chat_single(req: ChatPaperRequest) -> Dict[str, Any]:
    p = req.paper
    question = (req.question or "").strip()
    if not question:
        raise HTTPException(status_code=400, detail={"error": "empty_question"})
    cfg = _llm_config()
    _require_llm(cfg)

    answer = _call_chat(_chat_messages(p, question), cfg, "chat_failed")
    snippets = _pick_snippets(p.full_text or "", question)
    return {"answer": _strip_preamble(answer or ""), "evidence_snippets": snippets}


@app.post("/chat_stream")
def chat_stream(req: ChatPaperRequest) -> StreamingResponse:
    question = (req.question or "").strip()
    if not question:
        raise HTTPException(status_code=400, detail={"error": "empty_question"})
    cfg = _llm_config()
    _require_llm(cfg)
    return _ndjson_stream(_chat_messages(req.paper, question), cfg)


# --- analyze ---


_ANALYZE_FUNCS = {
    "year": analyze_mod.year_distribution,
    "authors": analyze_mod.top_authors,
    "keywords": analyze_mod.top_keywords,
    "journals": analyze_mod.journal_distribution,
    "cooccurrence": analyze_mod.keyword_cooccurrence,
    "tfidf": analyze_mod.tfidf_summary,
}


@app.post("/analyze")
def analyze(req: AnalyzeRequest) -> Dict[str, Any]:
    kind = (req.kind or "").strip().lower()
    if kind not in _ANALYZE_FUNCS:
        raise HTTPException(
            status_code=400,
            detail={"error": "unknown_kind", "kind": kind, "supported": list(_ANALYZE_FUNCS)},
        )
    fn = _ANALYZE_FUNCS[kind]
    params = req.params or {}
    try:
        conn = _get_conn()
    except Exception as e:
        raise HTTPException(status_code=500, detail={"error": "db_open_failed", "message": str(e)})
    try:
        if kind == "year":
            data = fn(conn)
        elif kind == "tfidf":
            data = fn(conn, top_k=int(params.get("top_k", 20)))
        elif kind in ("authors", "keywords", "journals", "cooccurrence"):
            data = fn(conn, n=int(params.get("n", 20 if kind == "authors" else 30)))
        else:  # pragma: no cover
            data = fn(conn)
    except Exception as e:
        traceback.print_exc()
        raise HTTPException(status_code=500, detail={"error": "analyze_failed", "message": str(e)})
    finally:
        try:
            conn.close()
        except Exception:
            pass

    return {"data": data}


@app.exception_handler(Exception)
async def _unhandled(request, exc):  # pragma: no cover
    traceback.print_exc()
    return JSONResponse(
        status_code=500,
        content={"error": "internal_error", "message": str(exc)},
    )
