"""Word 文档解析与切块。

切块采用两阶段策略：
  阶段一  按章节标题把论文粗分为章（正则识别"第X章 / 1 引言 / 一、…"等独立标题段落），
          并按章节标题关键词为整章打"段落作用"标签（概念解释/背景说明/方法依据/经验证据/研究空白）。
  阶段二  在章节内部做细切分，提供两种可切换的方法：
          - greedy   思路1：以段落为最小单位、基准 500 字的贪心装包；
          - semantic 思路2（默认）：句子级向量化 → 相邻句相似度谷值检测语义边界 →
                     语义段为原子单位贪心装包到 ~500 字（兼得主题完整与块长稳定）。
  legacy  旧的 500 字滑动窗口仍保留，便于对比。

注意：本数据集的 docx 多为 PDF 版式转换产物，"段落"经常在句中断开、且正文中混有
万方水印/页码/基金项目脚注等噪声段落，所以解析时先做噪声过滤，再把未以句末标点
结尾的段落与后文直接拼接，尽量恢复完整句子。
"""

from __future__ import annotations

import re
from typing import Any, Dict, List, Optional

_RESEARCH_DESIGN_START = ("研究设计", "研究方法", "数据来源", "实验设计", "模型构建")
_RESEARCH_DESIGN_END = (
    "模型分析",
    "实证分析",
    "结果分析",
    "实验结果",
    "数据分析",
    "讨论与结论",
)

# ---------------------------------------------------------------------------
# 段落读取与噪声过滤
# ---------------------------------------------------------------------------

# 整段噪声：万方水印 / 纯页码 / 纯数字横线
_NOISE_EXACT = {"万方数据"}
_NOISE_RE = re.compile(r"^[\s\-—–·.·]*\d{1,4}[\s\-—–·.·]*$")
# 脚注类段落（插在正文中间会把句子截断，干扰切块）
_FOOTNOTE_RE = re.compile(
    r"^(收稿日期|修回日期|基金项目|作者简介|通信作者|通讯作者|第一作者|文章编号|文献标识码|中图分类号|DOI[:：]|doi[:：])"
)


def _is_noise(text: str) -> bool:
    if text in _NOISE_EXACT:
        return True
    if _NOISE_RE.match(text):
        return True
    if _FOOTNOTE_RE.match(text):
        return True
    return False


def _read_paragraphs(path: str, keep_noise: bool = False) -> List[str]:
    from docx import Document  # python-docx, lazy import

    doc = Document(path)
    out: List[str] = []
    for p in doc.paragraphs:
        text = (p.text or "").strip()
        if not text:
            continue
        if not keep_noise and _is_noise(text):
            continue
        out.append(text)
    # 同时把表格中的文字也拉进来，避免漏掉关键信息
    for table in getattr(doc, "tables", []) or []:
        for row in table.rows:
            for cell in row.cells:
                cell_text = (cell.text or "").strip()
                if cell_text:
                    out.append(cell_text)
    return out


def _hits(line: str, markers) -> bool:
    if not line:
        return False
    for m in markers:
        if m in line:
            return True
    return False


def parse_docx(path: str) -> Dict[str, Any]:
    """解析 docx，返回 full_text/paragraphs/research_design_text。"""
    paragraphs = _read_paragraphs(path)
    full_text = "\n\n".join(paragraphs)

    collected: List[str] = []
    state = "idle"  # idle -> collecting -> done
    for line in paragraphs:
        if state == "idle":
            if _hits(line, _RESEARCH_DESIGN_START):
                state = "collecting"
                collected.append(line)
        elif state == "collecting":
            if _hits(line, _RESEARCH_DESIGN_END):
                state = "done"
                break
            collected.append(line)
        else:
            break

    research_design_text = "\n".join(collected).strip()
    return {
        "full_text": full_text,
        "paragraphs": paragraphs,
        "research_design_text": research_design_text,
    }


# ---------------------------------------------------------------------------
# 阶段一：章节标题识别
# ---------------------------------------------------------------------------

_CN_NUM = "零一二三四五六七八九十百两"
# 一级标题：第X章 / 第X部分
_H_DI_RE = re.compile(rf"^第[{_CN_NUM}\d]{{1,4}}[章节篇]|^第[{_CN_NUM}\d]{{1,4}}部分")
# 一级标题：一、xxx / 一．xxx
_H_CN_RE = re.compile(rf"^[{_CN_NUM}]{{1,3}}[、.．]\s*\S")
# 一级标题：1 xxx / 1、xxx / 1.xxx（注意排除 1.1 与年份等纯数字串）
_H_AR_RE = re.compile(r"^(\d{1,2})(?:\s+|[、]|[.．](?!\d))\s*(\S.*)$")
# 子标题（不作为章边界）：1.1 xxx / (一) xxx
_SUB_AR_RE = re.compile(r"^\d{1,2}([.．]\d{1,2}){1,3}")
_SUB_CN_RE = re.compile(rf"^[（(][{_CN_NUM}]{{1,3}}[)）]")
# 关键词式标题（无编号）
_H_KW = (
    "摘要", "引言", "绪论", "前言", "结语", "结论", "总结", "参考文献",
    "致谢", "附录", "文献综述", "相关工作", "研究方法", "研究设计",
    "讨论与结论", "结论与展望", "总结与展望", "研究展望",
)
_SENT_END = "。！？!?；;：:，,、"
_HAN_RE = re.compile(r"[一-鿿]")


def _is_chapter_heading(text: str) -> Optional[str]:
    """判断段落是否为一级章节标题；是则返回标题文本，否则 None。"""
    t = text.strip()
    if not t or len(t) > 40:
        return None
    if t[-1] in _SENT_END:
        return None
    # 子标题不是章边界
    if _SUB_AR_RE.match(t) or _SUB_CN_RE.match(t):
        return None
    if _H_DI_RE.match(t):
        return t
    if _H_CN_RE.match(t):
        return t
    m = _H_AR_RE.match(t)
    if m:
        rest = m.group(2)
        # 标题主体应以非数字开头且含至少 2 个汉字（排除 "2 0 2 5 年 6 期" 之类版式碎片）
        if rest and not rest[0].isdigit() and len(_HAN_RE.findall(rest)) >= 2:
            return t
    for kw in _H_KW:
        if t == kw or (t.startswith(kw) and len(t) <= len(kw) + 8):
            return t
    return None


# ---------------------------------------------------------------------------
# 章节级"段落作用"标签
# ---------------------------------------------------------------------------

SECTION_ROLES = ("概念解释", "背景说明", "方法依据", "经验证据", "研究空白")

# 有序规则表：先精确多字关键词，后泛化关键词；首个命中即生效
_ROLE_RULES = (
    # 非正文章节，标为"其他"（QA 取证时跳过）
    ("其他", ("参考文献", "致谢", "附录", "攻读", "作者简介", "个人简历")),
    # 文献综述/理论类 → 概念解释（先于"现状/分析"等泛词）
    ("概念解释", ("文献综述", "研究综述", "相关工作", "相关研究", "研究现状", "国内外现状",
                  "理论基础", "理论分析", "概念界定", "概念", "界定", "定义", "内涵", "述评", "综述")),
    # 结论/展望/对策 → 研究空白
    ("研究空白", ("结论与展望", "总结与展望", "研究展望", "展望", "不足", "局限",
                  "未来", "结论", "结语", "总结", "讨论", "对策", "建议", "启示",
                  "路径", "进路")),
    # 方法/模型/设计 → 方法依据（"实验设计/研究设计"须先于"实验"命中）
    ("方法依据", ("实验设计", "研究设计", "模型构建", "数据来源", "变量", "指标体系",
                  "研究方法", "方法", "模型", "算法", "框架", "构建", "测度", "量表")),
    # 实证/结果 → 经验证据
    ("经验证据", ("实证", "实验", "结果", "案例", "检验", "调查", "数据分析", "发现", "分析")),
    # 引言/背景 → 背景说明
    ("背景说明", ("引言", "绪论", "前言", "背景", "问题提出", "问题的提出", "动因", "缘起",
                  "摘要", "挑战", "困境", "现实问题")),
)


def classify_chapter_role(title: str, position: int = -1, total: int = -1):
    """按章节标题关键词判定段落作用。返回 (role, confidence)。

    未命中关键词时用位置兜底：首章→背景说明、末章→研究空白（confidence=medium），
    其余默认 概念解释（confidence=low），与设计文档一致。
    """
    t = (title or "").strip()
    for role, kws in _ROLE_RULES:
        for kw in kws:
            if kw in t:
                return role, "high"
    if total > 0 and position >= 0:
        if position <= 1:
            return "背景说明", "medium"
        if position >= total - 1:
            return "研究空白", "medium"
    return "概念解释", "low"


_CN_DIGITS = {"零": 0, "一": 1, "二": 2, "两": 2, "三": 3, "四": 4, "五": 5,
              "六": 6, "七": 7, "八": 8, "九": 9, "十": 10}


def _cn_to_int(s: str) -> Optional[int]:
    s = s.strip()
    if not s:
        return None
    if s.isdigit():
        return int(s)
    if len(s) == 1:
        return _CN_DIGITS.get(s)
    if s.startswith("十"):  # 十一..十九
        rest = _CN_DIGITS.get(s[1:], None) if len(s) == 2 else None
        return 10 + rest if rest is not None else None
    if s.endswith("十") and len(s) == 2:  # 二十/三十
        head = _CN_DIGITS.get(s[0])
        return head * 10 if head else None
    if len(s) == 3 and s[1] == "十":  # 二十一
        head, tail = _CN_DIGITS.get(s[0]), _CN_DIGITS.get(s[2])
        return head * 10 + tail if head and tail is not None else None
    return None


_ORD_DI_RE = re.compile(rf"^第([{_CN_NUM}\d]{{1,4}})[章节篇]|^第([{_CN_NUM}\d]{{1,4}})部分")
_ORD_CN_RE = re.compile(rf"^([{_CN_NUM}]{{1,3}})[、.．]")
_ORD_AR_RE = re.compile(r"^(\d{1,2})(?:\s+|[、]|[.．](?!\d))")


def _heading_ordinal(t: str):
    """提取标题的编号风格与序号：('di'|'cn'|'ar', int) 或 None。"""
    m = _ORD_DI_RE.match(t)
    if m:
        n = _cn_to_int(m.group(1) or m.group(2) or "")
        return ("di", n) if n is not None else None
    m = _ORD_CN_RE.match(t)
    if m:
        n = _cn_to_int(m.group(1))
        return ("cn", n) if n is not None else None
    m = _ORD_AR_RE.match(t)
    if m:
        return ("ar", int(m.group(1)))
    return None


def _toc_mask(paragraphs: List[str]) -> List[bool]:
    """标记目录区：连续 ≥3 个标题样式段落（学位论文目录）不作为章边界。"""
    is_h = [_is_chapter_heading(p) is not None for p in paragraphs]
    mask = [False] * len(paragraphs)
    i = 0
    while i < len(paragraphs):
        if is_h[i]:
            j = i
            while j < len(paragraphs) and is_h[j]:
                j += 1
            if j - i >= 3:
                for k in range(i, j):
                    mask[k] = True
            i = j
        else:
            i += 1
    return mask


def split_chapters(paragraphs: List[str]) -> List[Dict[str, Any]]:
    """把段落序列按一级章节标题切成章。返回 [{chapter_index,title,paragraphs}]。

    第一个标题之前的内容（题名/作者/摘要区）作为第 0 章"前置内容"。
    """
    in_toc = _toc_mask(paragraphs)
    chapters: List[Dict[str, Any]] = []
    cur_title = "前置内容"
    cur_paras: List[str] = []
    # 编号风格 → 已接受的最大序号；非"后继序号"的编号标题视为正文
    # （学位论文绪论里"第五章介绍…"之类的章节预告短行会伪装成标题）
    last_ord: Dict[str, int] = {}
    for pi, p in enumerate(paragraphs):
        heading = None if in_toc[pi] else _is_chapter_heading(p)
        if heading is not None:
            ordinal = _heading_ordinal(heading)
            if ordinal is not None:
                style, num = ordinal
                prev = last_ord.get(style)
                ok = num in (0, 1) if prev is None else num == prev + 1
                if ok:
                    last_ord[style] = num
                else:
                    heading = None
        if heading is not None:
            if cur_paras or chapters:
                chapters.append({"title": cur_title, "paragraphs": cur_paras})
            else:
                # 首章之前没有任何内容时，跳过空的"前置内容"
                pass
            cur_title = heading
            cur_paras = []
        else:
            cur_paras.append(p)
    chapters.append({"title": cur_title, "paragraphs": cur_paras})

    out: List[Dict[str, Any]] = []
    total = len(chapters)
    for i, ch in enumerate(chapters):
        if not ch["paragraphs"] and ch["title"] == "前置内容":
            continue
        role, conf = classify_chapter_role(ch["title"], position=i, total=total)
        if ch["title"] == "前置内容":
            role, conf = "背景说明", "medium"
        out.append(
            {
                "chapter_index": len(out),
                "title": ch["title"],
                "paragraphs": ch["paragraphs"],
                "section_role": role,
                "tag_confidence": conf,
            }
        )
    return out


# ---------------------------------------------------------------------------
# 阶段二：章节内细切分
# ---------------------------------------------------------------------------

_TERMINAL = "。！？!?；;…"
# 中文句末标点直接切；英文句号要求后跟空白（避免切碎小数/缩写）
_SENT_SPLIT_RE = re.compile(r"(?<=[。！？!?；;])|(?<=\.)\s+")

# 切块的硬上限：bge 嵌入窗口约 512 token（中文 ≈ 512 字），超出部分参与不了向量召回
_CHUNK_HARD_MAX = 620
_CHUNK_MIN_TAIL = 120


def _join_fragments(paragraphs: List[str]) -> str:
    """把版式碎片段落拼回连续文本：未以句末标点结尾的段落与后文直接相连。"""
    parts: List[str] = []
    for p in paragraphs:
        t = p.strip()
        if not t:
            continue
        parts.append(t)
        parts.append("\n" if t[-1] in _TERMINAL else "")
    return "".join(parts).strip()


def _split_sentences(text: str) -> List[str]:
    """按句末标点切句；换行不作为句界（版式断行会在句中出现）。"""
    flat = re.sub(r"\s*\n\s*", "", text)
    sents = [s.strip() for s in _SENT_SPLIT_RE.split(flat) if s and s.strip()]
    return sents


def _pack_units(units: List[str], base: int = 500, hard_max: int = _CHUNK_HARD_MAX) -> List[str]:
    """把原子单元（段落或语义段）贪心装包到接近 base 字。

    思路1 的决策规则：读入下一单元会超过 base 时，比较"装入/不装入"哪种长度更接近 base，
    且合并后不超过 hard_max。单个超长单元在句子边界处再切。
    章尾不足 _CHUNK_MIN_TAIL 字的残余并入前一块，避免碎块。
    """
    chunks: List[str] = []
    buf = ""
    for u in units:
        if len(u) > hard_max:
            if buf:
                chunks.append(buf)
                buf = ""
            chunks.extend(_split_oversize(u, base, hard_max))
            continue
        if not buf:
            buf = u
            continue
        merged = len(buf) + len(u)
        if merged <= base:
            buf += u
        elif merged <= hard_max and abs(merged - base) <= abs(len(buf) - base):
            buf += u
        else:
            chunks.append(buf)
            buf = u
    if buf:
        if chunks and len(buf) < _CHUNK_MIN_TAIL and len(chunks[-1]) + len(buf) <= hard_max:
            chunks[-1] += buf
        else:
            chunks.append(buf)
    return chunks


def _split_oversize(text: str, base: int, hard_max: int = _CHUNK_HARD_MAX) -> List[str]:
    """超长单元按句子边界切到 ~base 字；仍超长的"句子"（表格/英文摘要等）硬切。"""
    sents: List[str] = []
    for s in _split_sentences(text):
        if len(s) > hard_max:
            sents.extend(s[i : i + base] for i in range(0, len(s), base))
        else:
            sents.append(s)
    if not sents:
        return []
    if len(sents) == 1:
        return sents
    return _pack_units(sents, base=base, hard_max=hard_max)


def chunk_chapter_greedy(paragraphs: List[str], base: int = 500) -> List[str]:
    """思路1：以段落为最小单位的长度贪心切分。"""
    units = [p.strip() for p in paragraphs if p.strip()]
    return _pack_units(units, base=base)


def chunk_chapter_semantic(
    paragraphs: List[str],
    embedder,
    base: int = 500,
    min_seg_sents: int = 3,
    max_seg_sents: int = 15,
) -> List[str]:
    """思路2（增强）：语义边界检测 + 语义段贪心装包。

    1. 拼回碎片段落并按句末标点切句；
    2. 句子向量化（与正式 chunk 嵌入同一 bge 模型）；
    3. 相邻句余弦相似度低于 mean-std 处为候选边界（段最短 min_seg_sents 句）；
    4. 超过 max_seg_sents 句无边界的长跑道，在其相似度最低处强制切开；
    5. 语义段为原子单位，贪心装包到 ~base 字。
    """
    text = _join_fragments(paragraphs)
    sents = _split_sentences(text)
    if len(sents) < min_seg_sents * 2:
        return _pack_units(sents, base=base) if sents else []

    import numpy as np

    vecs = np.asarray(embedder.embed(sents), dtype=np.float32)
    # bge 输出已归一化，点积即余弦
    sims = np.sum(vecs[:-1] * vecs[1:], axis=1)
    threshold = float(np.mean(sims) - np.std(sims))

    boundaries: List[int] = []
    last = 0
    for i, sim in enumerate(sims):
        if sim < threshold and (i + 1 - last) >= min_seg_sents:
            boundaries.append(i + 1)
            last = i + 1
    # 强制上限：超长跑道在最低相似度处切开
    final: List[int] = []
    prev = 0
    for b in boundaries + [len(sents)]:
        while b - prev > max_seg_sents:
            window = sims[prev + min_seg_sents - 1 : prev + max_seg_sents]
            if len(window) == 0:
                break
            cut = prev + min_seg_sents + int(np.argmin(window))
            final.append(cut)
            prev = cut
        if b < len(sents):
            final.append(b)
        prev = b

    segments: List[str] = []
    start = 0
    for b in final + [len(sents)]:
        if b <= start:
            continue
        segments.append("".join(sents[start:b]))
        start = b
    return _pack_units(segments, base=base)


def chunk_paper(
    parsed: Dict[str, Any],
    method: str = "semantic",
    embedder=None,
    base: int = 500,
) -> List[Dict[str, Any]]:
    """两阶段切分入口：章节切分 + 章内细切分 + 角色标签。

    返回与旧 sliding_chunks 兼容的 chunk 字典列表，并附加
    chapter_title/chapter_index/section_role/tag_confidence/split_method。
    """
    if method == "legacy":
        out = sliding_chunks(parsed.get("full_text") or "")
        for c in out:
            c.update(
                chapter_title="",
                chapter_index=-1,
                section_role="",
                tag_confidence="",
                split_method="legacy_window",
            )
        return out

    chapters = split_chapters(parsed.get("paragraphs") or [])
    if method == "semantic" and embedder is None:
        method = "greedy"
    split_method = "semantic_boundary" if method == "semantic" else "length_greedy"

    out: List[Dict[str, Any]] = []
    offset = 0
    idx = 0
    for ch in chapters:
        if method == "semantic":
            texts = chunk_chapter_semantic(ch["paragraphs"], embedder, base=base)
        else:
            texts = chunk_chapter_greedy(ch["paragraphs"], base=base)
        for t in texts:
            t = t.strip()
            if not t:
                continue
            out.append(
                {
                    "chunk_index": idx,
                    "offset_start": offset,
                    "paragraph_index": -1,
                    "chunk_text": t,
                    "chapter_title": ch["title"],
                    "chapter_index": ch["chapter_index"],
                    "section_role": ch["section_role"],
                    "tag_confidence": ch["tag_confidence"],
                    "split_method": split_method,
                }
            )
            offset += len(t)
            idx += 1
    return out


def sliding_chunks(text: str, size: int = 500, overlap: int = 100) -> List[Dict[str, Any]]:
    """按字符滑动窗口切块（旧方案，保留作对比/降级）。"""
    if not text:
        return []
    if size <= 0:
        raise ValueError("size must be positive")
    if overlap < 0 or overlap >= size:
        overlap = max(0, min(overlap, size - 1))
    step = size - overlap
    n = len(text)
    out: List[Dict[str, Any]] = []
    idx = 0
    pos = 0
    while pos < n:
        end = min(pos + size, n)
        chunk_text = text[pos:end]
        out.append(
            {
                "chunk_index": idx,
                "offset_start": pos,
                "paragraph_index": -1,
                "chunk_text": chunk_text,
            }
        )
        idx += 1
        if end >= n:
            break
        pos += step
    return out


__all__ = [
    "parse_docx",
    "sliding_chunks",
    "chunk_paper",
    "split_chapters",
    "classify_chapter_role",
    "SECTION_ROLES",
]
