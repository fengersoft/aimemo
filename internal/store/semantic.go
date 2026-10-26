package store

import (
	"database/sql"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// featureVector is a sparse term->weight map used for lightweight semantic
// matching. It is intentionally not a full embedding model: it works offline
// with no external dependencies, supports CJK and English, and gives
// "semantic-ish" recall that goes beyond exact keyword matching.
type featureVector map[uint32]float32

// fnv1a hashes a string to a uint32 using FNV-1a.
func fnv1a(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// extractFeatures converts text into a sparse feature vector.
//
// Features are:
//   - word unigrams (lowercased, for Latin scripts)
//   - character bigrams (for CJK and mixed text)
//   - character trigrams (for CJK, to capture longer phrases)
//
// Weights use sublinear TF scaling: w = 1 + log(tf).
func extractFeatures(text string) featureVector {
	fv := featureVector{}
	if text == "" {
		return fv
	}
	lower := strings.ToLower(text)

	// word unigrams
	for _, word := range strings.FieldsFunc(lower, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}) {
		if utf8.RuneCountInString(word) >= 2 {
			fv[fnv1a("w:"+word)]++
		}
	}

	// character bigrams and trigrams for CJK runs
	runes := []rune(lower)
	for i := 0; i < len(runes); i++ {
		if IsCJK(runes[i]) {
			if i+1 < len(runes) && IsCJK(runes[i+1]) {
				bg := string(runes[i : i+2])
				fv[fnv1a("b:"+bg)]++
			}
			if i+2 < len(runes) && IsCJK(runes[i+1]) && IsCJK(runes[i+2]) {
				tg := string(runes[i : i+3])
				fv[fnv1a("t:"+tg)]++
			}
		}
	}

	// sublinear TF scaling + normalize to unit vector
	var norm float64
	for k, v := range fv {
		w := float32(1 + math.Log(float64(v)))
		fv[k] = w
		norm += float64(w) * float64(w)
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for k, v := range fv {
			fv[k] = v * scale
		}
	}
	return fv
}

// synonyms maps common technical terms to related terms. It is a lightweight
// offline substitute for a full embedding model: when the query contains a
// key, the synonyms are added to the query features as well, improving recall
// for "same idea, different words" queries.
var synonyms = map[string][]string{
	"parallel":   {"concurrent", "goroutine", "thread", "async"},
	"concurrent": {"parallel", "goroutine", "thread", "async"},
	"goroutine":  {"concurrent", "parallel", "channel", "thread"},
	"thread":     {"concurrent", "parallel", "goroutine"},
	"async":      {"concurrent", "parallel", "await", "promise"},
	"并发":        {"并行", "协程", "线程", "异步"},
	"并行":        {"并发", "协程", "线程"},
	"协程":        {"并发", "goroutine", "channel"},
	"error":      {"exception", "panic", "failure", "错误"},
	"exception":  {"error", "panic", "catch", "异常"},
	"panic":      {"error", "crash", "recover", "崩溃"},
	"错误":        {"异常", "panic", "error"},
	"异常":        {"错误", "exception", "error"},
	"崩溃":        {"panic", "crash"},
	"database":   {"db", "sql", "sqlite", "mysql", "postgres", "存储"},
	"db":         {"database", "sql", "存储"},
	"sql":        {"database", "query", "数据库"},
	"数据库":       {"存储", "db", "sql", "持久化"},
	"缓存":        {"cache", "redis", "memoization"},
	"cache":      {"缓存", "redis", "memoization"},
	"performance": {"speed", "fast", "optimize", "性能"},
	"optimize":   {"performance", "speed", "性能优化"},
	"memory":     {"leak", "gc", "alloc", "内存"},
	"内存":        {"memory", "泄漏", "gc"},
	"泄漏":        {"leak", "内存", "memory"},
	"test":       {"testing", "unit", "integration", "测试"},
	"测试":        {"test", "unit", "单元测试"},
	"security":   {"vulnerability", "xss", "injection", "安全"},
	"安全":        {"security", "漏洞", "xss"},
	"漏洞":        {"vulnerability", "security", "安全"},
	"api":        {"endpoint", "rest", "http", "接口"},
	"接口":        {"api", "endpoint", "rest"},
	"config":     {"configuration", "settings", "配置"},
	"配置":        {"config", "settings"},
	"deploy":     {"deployment", "release", "部署"},
	"部署":        {"deploy", "release"},
	"build":      {"compile", "make", "构建"},
	"构建":        {"build", "compile"},
	"dependency": {"package", "module", "依赖"},
	"依赖":        {"dependency", "package"},
	"log":        {"logging", "logger", "日志"},
	"日志":        {"log", "logging"},
	"refactor":   {"cleanup", "restructure", "重构"},
	"重构":        {"refactor", "cleanup"},
	"bug":        {"defect", "issue", "问题"},
	"问题":        {"bug", "issue", "defect"},
	"fix":        {"bug", "patch", "修复"},
	"修复":        {"fix", "patch"},
}

// extractFeaturesWithSynonyms converts text into a feature vector and adds
// synonym features for any known term found in the text.
func extractFeaturesWithSynonyms(text string) featureVector {
	base := extractFeatures(text)
	lower := strings.ToLower(text)
	for word, syns := range synonyms {
		if strings.Contains(lower, word) {
			for _, syn := range syns {
				synFv := extractFeatures(syn)
				for k, v := range synFv {
					base[k] += v * 0.5
				}
			}
		}
	}
	var norm float64
	for _, v := range base {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for k, v := range base {
			base[k] = v * scale
		}
	}
	return base
}

// cosineSimilarity returns the dot product of two unit-normalized sparse
// vectors (both are already normalized by extractFeatures).
func cosineSimilarity(a, b featureVector) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// iterate over the smaller map
	if len(a) > len(b) {
		a, b = b, a
	}
	var dot float64
	for k, va := range a {
		if vb, ok := b[k]; ok {
			dot += float64(va) * float64(vb)
		}
	}
	return dot
}

// serializeFeatures encodes a feature vector into a compact binary blob.
//
// Format: uint32 count, then count pairs of (uint32 key, float32 value).
func serializeFeatures(fv featureVector) []byte {
	if len(fv) == 0 {
		return nil
	}
	// sort keys for deterministic output
	keys := make([]uint32, 0, len(fv))
	for k := range fv {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	buf := make([]byte, 4+len(keys)*8)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(keys)))
	for i, k := range keys {
		off := 4 + i*8
		binary.LittleEndian.PutUint32(buf[off:off+4], k)
		binary.LittleEndian.PutUint32(buf[off+4:off+8], math.Float32bits(fv[k]))
	}
	return buf
}

// deserializeFeatures decodes a binary blob back into a feature vector.
func deserializeFeatures(data []byte) featureVector {
	if len(data) < 4 {
		return featureVector{}
	}
	n := binary.LittleEndian.Uint32(data[0:4])
	if int(n)*8+4 > len(data) {
		return featureVector{}
	}
	fv := make(featureVector, n)
	for i := uint32(0); i < n; i++ {
		off := 4 + int(i)*8
		k := binary.LittleEndian.Uint32(data[off : off+4])
		v := math.Float32frombits(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		fv[k] = v
	}
	return fv
}

// indexFeatures writes or replaces the feature vector for a memory. It must
// be called inside the same transaction as the main table write.
func indexFeatures(q querier, id int64, text string) error {
	fv := extractFeatures(text)
	blob := serializeFeatures(fv)
	_, err := q.Exec(`UPDATE memories SET features = ? WHERE id = ?`, blob, id)
	return err
}

// SemanticResult pairs a memory with its semantic similarity score.
type SemanticResult struct {
	Memory Memory  `json:"memory"`
	Score  float64 `json:"score"`
}

// SearchSemantic finds memories whose feature vectors are similar to the
// query. It loads candidate vectors (bounded by maxScan) and ranks by cosine
// similarity. This complements FTS search when exact keywords differ.
func (s *Store) SearchSemantic(query string, f Filter, limit, maxScan int) ([]SemanticResult, error) {
	if maxScan <= 0 || maxScan > 5000 {
		maxScan = 1000
	}
	qfv := extractFeaturesWithSynonyms(query)
	if len(qfv) == 0 {
		return nil, nil
	}

	// Load candidates that have features. The filter is applied via a
	// lightweight join so workspace scoping still holds.
	where, args := f.clauses()
	rows, err := s.db.Query(`
		SELECT `+selectMemoryColumns+`, m.features
		FROM memories m JOIN categories c ON c.id = m.category_id
		WHERE m.features IS NOT NULL`+where+`
		ORDER BY m.id DESC LIMIT ?`, append(args, maxScan)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type cand struct {
		mem Memory
		fv  featureVector
	}
	var cands []cand
	for rows.Next() {
		var m Memory
		var catID int64
		var workspace, problem, solution, pitfall, example, scope, source, validUntil, supersedes, lastUsed, createdAt, updatedAt string
		var confidence, useCount, helpfulCount, wrongCount int
		var features []byte
		if err := rows.Scan(&m.ID, &catID, &m.Category, &m.Type, &m.Importance,
			&m.Title, &m.Content, &problem, &solution, &pitfall, &example,
			&m.Scope, &m.Source, &workspace, &confidence, &validUntil, &supersedes,
			&lastUsed, &useCount, &helpfulCount, &wrongCount,
			&createdAt, &updatedAt, &features); err != nil {
			return nil, err
		}
		m.CategoryID = catID
		m.Problem = problem
		m.Solution = solution
		m.Pitfall = pitfall
		m.Example = example
		m.Scope = scope
		m.Source = source
		m.Workspace = workspace
		m.Confidence = confidence
		m.ValidUntil = validUntil
		m.LastUsedAt = lastUsed
		m.UseCount = useCount
		m.HelpfulCount = helpfulCount
		m.WrongCount = wrongCount
		m.CreatedAt = createdAt
		m.UpdatedAt = updatedAt
		fv := deserializeFeatures(features)
		cands = append(cands, cand{mem: m, fv: fv})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// score and rank
	results := make([]SemanticResult, 0, len(cands))
	for _, c := range cands {
		score := cosineSimilarity(qfv, c.fv)
		if score > 0.01 {
			results = append(results, SemanticResult{Memory: c.mem, Score: score})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Memory.ID > results[j].Memory.ID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	// attach tags
	memories := make([]Memory, len(results))
	for i := range results {
		memories[i] = results[i].Memory
	}
	if err := attachTags(s.db, memories); err != nil {
		return nil, err
	}
	for i := range results {
		results[i].Memory = memories[i]
	}
	return results, nil
}

// RebuildFeatures recomputes the feature vector for every memory and returns
// the number of rows updated. It backs doctor --rebuild.
func (s *Store) RebuildFeatures() (int, error) {
	type row struct {
		id   int64
		body string
	}
	rows, err := s.db.Query(`SELECT id, title, content, problem, solution, pitfall, example FROM memories`)
	if err != nil {
		return 0, err
	}
	var all []row
	for rows.Next() {
		var (
			r                                            row
			title, content, problem, solution, pitfall, example string
		)
		if err := rows.Scan(&r.id, &title, &content, &problem, &solution, &pitfall, &example); err != nil {
			rows.Close()
			return 0, err
		}
		r.body = title + "\n" + structuredBody(content, problem, solution, pitfall, example)
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	err = s.withTx(func(tx *sql.Tx) error {
		for _, r := range all {
			fv := extractFeatures(r.body)
			blob := serializeFeatures(fv)
			if _, err := tx.Exec(`UPDATE memories SET features = ? WHERE id = ?`, blob, r.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(all), nil
}
