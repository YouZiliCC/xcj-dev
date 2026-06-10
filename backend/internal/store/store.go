package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "modernc.org/sqlite"
)

type Paper struct {
	PaperID              string `json:"paper_id"`
	Title                string `json:"title"`
	DOI                  string `json:"doi"`
	PublishYear          int    `json:"publish_year"`
	Author               string `json:"author"`
	Keywords             string `json:"keywords"`
	Abstract             string `json:"abstract"`
	SourceJournal        string `json:"source_journal"`
	Affiliation          string `json:"affiliation"`
	CoreType             string `json:"core_type"`
	IsCore               int    `json:"is_core"`
	CLCNumber            string `json:"clc_number"`
	ResearchDesignText   string `json:"research_design_text"`
	TitleTokens          string `json:"-"`
	KeywordsTokens       string `json:"-"`
	AbstractTokens       string `json:"-"`
	ResearchDesignTokens string `json:"-"`
	BodyTokens           string `json:"-"`
	RawBody              string `json:"-"`
}

type Chunk struct {
	ChunkID        string    `json:"chunk_id"`
	PaperID        string    `json:"paper_id"`
	ChunkIndex     int       `json:"chunk_index"`
	ParagraphIndex int       `json:"paragraph_index"`
	OffsetStart    int       `json:"offset_start"`
	ChunkText      string    `json:"chunk_text"`
	ChapterTitle   string    `json:"chapter_title"`
	ChapterIndex   int       `json:"chapter_index"`
	SectionRole    string    `json:"section_role"`
	TagConfidence  string    `json:"tag_confidence"`
	SplitMethod    string    `json:"split_method"`
	Embedding      []float32 `json:"-"`
}

type DB struct {
	*sql.DB
	driver string
}

func Open(driver, dsn string) (*DB, error) {
	if driver == "sqlite" {
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return nil, err
		}
	}
	conn, err := sql.Open(driverName(driver), dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(8)
	conn.SetConnMaxLifetime(30 * time.Minute)
	if err := conn.Ping(); err != nil {
		return nil, err
	}
	return &DB{DB: conn, driver: driver}, nil
}

func driverName(d string) string {
	if d == "sqlite" {
		return "sqlite"
	}
	return d
}

func (d *DB) Driver() string { return d.driver }

// Migrate 应用迁移脚本。MySQL 需将 AUTOINCREMENT 重写为 AUTO_INCREMENT。
func (d *DB) Migrate(migrationsDir string) error {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(migrationsDir, e.Name()))
		if err != nil {
			return err
		}
		sqlText := string(raw)
		if d.driver == "mysql" {
			sqlText = strings.ReplaceAll(sqlText, "AUTOINCREMENT", "AUTO_INCREMENT")
			sqlText = strings.ReplaceAll(sqlText, "BLOB", "MEDIUMBLOB")
		}
		for _, stmt := range splitSQL(sqlText) {
			s := strings.TrimSpace(stmt)
			if s == "" {
				continue
			}
			if _, err := d.Exec(s); err != nil {
				// 容忍幂等 DDL：重复执行 ALTER ... ADD COLUMN 时两种驱动分别报
				// "duplicate column name"(sqlite) / "Duplicate column name"(mysql)。
				if isDuplicateColumnErr(err) {
					continue
				}
				return fmt.Errorf("migration %s: %w\n%s", e.Name(), err, s)
			}
		}
	}
	return nil
}

func isDuplicateColumnErr(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func splitSQL(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ";") {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

// --- Papers ---

func (d *DB) UpsertPaper(p Paper) error {
	q := `INSERT INTO papers_master
(paper_id,title,doi,publish_year,author,keywords,abstract,source_journal,affiliation,core_type,is_core,clc_number,research_design_text,
 title_tokens,keywords_tokens,abstract_tokens,research_design_tokens,body_tokens,raw_body)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if d.driver == "sqlite" {
		q += ` ON CONFLICT(paper_id) DO UPDATE SET
title=excluded.title, doi=excluded.doi, publish_year=excluded.publish_year,
author=excluded.author, keywords=excluded.keywords, abstract=excluded.abstract,
source_journal=excluded.source_journal, affiliation=excluded.affiliation,
core_type=excluded.core_type, is_core=excluded.is_core, clc_number=excluded.clc_number,
research_design_text=excluded.research_design_text,
title_tokens=excluded.title_tokens, keywords_tokens=excluded.keywords_tokens,
abstract_tokens=excluded.abstract_tokens, research_design_tokens=excluded.research_design_tokens,
body_tokens=excluded.body_tokens, raw_body=excluded.raw_body`
	} else {
		q += ` ON DUPLICATE KEY UPDATE
title=VALUES(title), doi=VALUES(doi), publish_year=VALUES(publish_year),
author=VALUES(author), keywords=VALUES(keywords), abstract=VALUES(abstract),
source_journal=VALUES(source_journal), affiliation=VALUES(affiliation),
core_type=VALUES(core_type), is_core=VALUES(is_core), clc_number=VALUES(clc_number),
research_design_text=VALUES(research_design_text),
title_tokens=VALUES(title_tokens), keywords_tokens=VALUES(keywords_tokens),
abstract_tokens=VALUES(abstract_tokens), research_design_tokens=VALUES(research_design_tokens),
body_tokens=VALUES(body_tokens), raw_body=VALUES(raw_body)`
	}
	_, err := d.Exec(q,
		p.PaperID, p.Title, p.DOI, p.PublishYear, p.Author, p.Keywords, p.Abstract, p.SourceJournal, p.Affiliation,
		p.CoreType, p.IsCore, p.CLCNumber, p.ResearchDesignText,
		p.TitleTokens, p.KeywordsTokens, p.AbstractTokens, p.ResearchDesignTokens, p.BodyTokens, p.RawBody)
	return err
}

// 选用 COALESCE 把 NULL 列归零，避免 modernc/sqlite Scan 到 *string/*int 时报错。
const paperSelect = `SELECT paper_id,
COALESCE(title,''), COALESCE(doi,''), COALESCE(publish_year,0),
COALESCE(author,''), COALESCE(keywords,''), COALESCE(abstract,''),
COALESCE(source_journal,''), COALESCE(affiliation,''),
COALESCE(core_type,''), COALESCE(is_core,0), COALESCE(clc_number,''),
COALESCE(research_design_text,''),
COALESCE(title_tokens,''), COALESCE(keywords_tokens,''), COALESCE(abstract_tokens,''),
COALESCE(research_design_tokens,''), COALESCE(body_tokens,''), COALESCE(raw_body,'')
FROM papers_master`

func scanPaper(scan func(...any) error) (Paper, error) {
	var p Paper
	err := scan(&p.PaperID, &p.Title, &p.DOI, &p.PublishYear, &p.Author, &p.Keywords, &p.Abstract, &p.SourceJournal,
		&p.Affiliation, &p.CoreType, &p.IsCore, &p.CLCNumber, &p.ResearchDesignText,
		&p.TitleTokens, &p.KeywordsTokens, &p.AbstractTokens, &p.ResearchDesignTokens, &p.BodyTokens, &p.RawBody)
	return p, err
}

func (d *DB) GetPaper(id string) (*Paper, error) {
	row := d.QueryRow(paperSelect+` WHERE paper_id=?`, id)
	p, err := scanPaper(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *DB) AllPapers() ([]Paper, error) {
	rows, err := d.Query(paperSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Paper
	for rows.Next() {
		p, err := scanPaper(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) CountPapers() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM papers_master`).Scan(&n)
	return n, err
}

// --- Chunks ---

func (d *DB) UpsertChunk(c Chunk) error {
	emb := EncodeVector(c.Embedding)
	q := `INSERT INTO paper_chunks
(chunk_id,paper_id,chunk_index,paragraph_index,offset_start,chunk_text,chapter_title,chapter_index,section_role,tag_confidence,split_method,embedding)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`
	if d.driver == "sqlite" {
		q += ` ON CONFLICT(chunk_id) DO UPDATE SET
paper_id=excluded.paper_id, chunk_index=excluded.chunk_index,
paragraph_index=excluded.paragraph_index, offset_start=excluded.offset_start,
chunk_text=excluded.chunk_text, chapter_title=excluded.chapter_title, chapter_index=excluded.chapter_index,
section_role=excluded.section_role, tag_confidence=excluded.tag_confidence, split_method=excluded.split_method,
embedding=excluded.embedding`
	} else {
		q += ` ON DUPLICATE KEY UPDATE
paper_id=VALUES(paper_id), chunk_index=VALUES(chunk_index),
paragraph_index=VALUES(paragraph_index), offset_start=VALUES(offset_start),
chunk_text=VALUES(chunk_text), chapter_title=VALUES(chapter_title), chapter_index=VALUES(chapter_index),
section_role=VALUES(section_role), tag_confidence=VALUES(tag_confidence), split_method=VALUES(split_method),
embedding=VALUES(embedding)`
	}
	_, err := d.Exec(q, c.ChunkID, c.PaperID, c.ChunkIndex, c.ParagraphIndex, c.OffsetStart, c.ChunkText,
		c.ChapterTitle, c.ChapterIndex, c.SectionRole, c.TagConfidence, c.SplitMethod, emb)
	return err
}

const chunkSelect = `SELECT chunk_id,paper_id,COALESCE(chunk_index,0),COALESCE(paragraph_index,0),COALESCE(offset_start,0),COALESCE(chunk_text,''),
COALESCE(chapter_title,''),COALESCE(chapter_index,-1),COALESCE(section_role,''),COALESCE(tag_confidence,''),COALESCE(split_method,''),embedding
FROM paper_chunks`

func scanChunk(scan func(...any) error) (Chunk, error) {
	var c Chunk
	var emb []byte
	err := scan(&c.ChunkID, &c.PaperID, &c.ChunkIndex, &c.ParagraphIndex, &c.OffsetStart, &c.ChunkText,
		&c.ChapterTitle, &c.ChapterIndex, &c.SectionRole, &c.TagConfidence, &c.SplitMethod, &emb)
	if err == nil {
		c.Embedding = DecodeVector(emb)
	}
	return c, err
}

func (d *DB) AllChunks() ([]Chunk, error) {
	rows, err := d.Query(chunkSelect)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		c, err := scanChunk(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) ChunksByPaper(paperID string) ([]Chunk, error) {
	rows, err := d.Query(chunkSelect+` WHERE paper_id=? ORDER BY chunk_index`, paperID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		c, err := scanChunk(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CountChunks() (int, error) {
	var n int
	err := d.QueryRow(`SELECT COUNT(*) FROM paper_chunks`).Scan(&n)
	return n, err
}

// --- History ---

type SearchHistory struct {
	ID        int64     `json:"id"`
	Mode      string    `json:"mode"`
	Query     string    `json:"query"`
	Filters   string    `json:"filters"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) AddHistory(mode, query, filters string) error {
	_, err := d.Exec(`INSERT INTO search_history(mode,query_text,filters) VALUES (?,?,?)`, mode, query, filters)
	return err
}

func (d *DB) RecentHistory(limit int) ([]SearchHistory, error) {
	rows, err := d.Query(`SELECT id,mode,query_text,filters,created_at FROM search_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchHistory
	for rows.Next() {
		var h SearchHistory
		var q, f sql.NullString
		if err := rows.Scan(&h.ID, &h.Mode, &q, &f, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.Query = q.String
		h.Filters = f.String
		out = append(out, h)
	}
	return out, rows.Err()
}

// --- Vector codec ---

// EncodeVector: 将 []float32 编码为 base64(little-endian float32 bytes)。
// 这样既能在 SQLite/MySQL 的 BLOB 列里存，也能在 JSON 里走 base64 字符串。
func EncodeVector(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	encoded := base64.StdEncoding.EncodeToString(buf)
	return []byte(encoded)
}

func DecodeVector(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(b))
	if err != nil || len(raw)%4 != 0 {
		return nil
	}
	out := make([]float32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return out
}
