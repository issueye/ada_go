package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"
)

// Embedder 将文本转换为向量，是记忆引擎与嵌入模型之间的抽象。
type Embedder interface {
	// Name 返回提供者标识，用于日志与统计信息。
	Name() string
	// Dim 返回向量维度（集合按维度命名，切换提供者不会互相污染）。
	Dim() int
	// Embed 批量向量化文本，返回与入参顺序一致的向量。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// DedupThresholdAware 是 Embedder 的可选扩展接口：
// 由提供者声明其向量分布下的默认语义去重阈值。
// 不同嵌入器的相似度分布差异巨大（密集模型近重复 >0.9，
// 词面哈希相近文本仅 0.6 左右），固定阈值无法适配所有提供者。
type DedupThresholdAware interface {
	DefaultDedupThreshold() float64
}

// ---------------------------------------------------------------
// OpenAI 兼容嵌入器
// ---------------------------------------------------------------

// OpenAIEmbedder 调用任意 OpenAI /embeddings 兼容接口
// （OpenAI、Azure 代理、Ollama、vLLM、SiliconFlow 等）。
type OpenAIEmbedder struct {
	baseURL string // 形如 https://api.openai.com/v1，不带尾部斜杠
	apiKey  string
	model   string
	dim     int
	client  *http.Client
}

// embeddingsRequest 是 /embeddings 请求体。
type embeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embeddingsResponse 是 /embeddings 响应体（只解析所需字段）。
type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// NewOpenAIEmbedder 创建 OpenAI 兼容嵌入器，并通过一次探测请求确定向量维度。
// 探测失败（网络不通、Key 无效等）会返回错误，避免以错误维度建库。
func NewOpenAIEmbedder(ctx context.Context, baseURL, apiKey, model string) (*OpenAIEmbedder, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	e := &OpenAIEmbedder{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
	vecs, err := e.Embed(ctx, []string{"dim probe"})
	if err != nil {
		return nil, fmt.Errorf("嵌入模型探测失败（请检查 MEM_EMBEDDING_BASE_URL / API Key / 模型名）: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("嵌入模型 %s 返回了空向量", model)
	}
	e.dim = len(vecs[0])
	return e, nil
}

// Name 实现 Embedder。
func (e *OpenAIEmbedder) Name() string { return "openai:" + e.model }

// Dim 实现 Embedder。
func (e *OpenAIEmbedder) Dim() int { return e.dim }

// DefaultDedupThreshold 实现 DedupThresholdAware：密集嵌入的近重复通常 >0.9。
func (e *OpenAIEmbedder) DefaultDedupThreshold() float64 { return 0.93 }

// Embed 实现 Embedder，按批调用 /embeddings 并按 index 还原顺序。
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embeddingsRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed embeddingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析嵌入响应失败（HTTP %d）: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		msg := http.StatusText(resp.StatusCode)
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return nil, fmt.Errorf("嵌入接口返回 HTTP %d: %s", resp.StatusCode, msg)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("嵌入接口返回 %d 条向量，期望 %d 条", len(parsed.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("嵌入接口返回越界的 index %d", d.Index)
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}

// ---------------------------------------------------------------
// 离线哈希嵌入器（零依赖回退方案）
// ---------------------------------------------------------------

// HashEmbedder 是确定性的字符 n-gram 特征哈希嵌入器，
// 无需网络与 API Key，适用于离线演示、单元测试或网络受限环境。
// 它只刻画词面相似度，语义泛化能力弱于真实嵌入模型。
type HashEmbedder struct {
	dim int
}

// DefaultHashDim 是 HashEmbedder 的默认维度。
const DefaultHashDim = 512

// NewHashEmbedder 创建离线哈希嵌入器，dim 最小为 64。
func NewHashEmbedder(dim int) *HashEmbedder {
	if dim < 64 {
		dim = DefaultHashDim
	}
	return &HashEmbedder{dim: dim}
}

// Name 实现 Embedder。
func (e *HashEmbedder) Name() string { return fmt.Sprintf("hash:%d", e.dim) }

// Dim 实现 Embedder。
func (e *HashEmbedder) Dim() int { return e.dim }

// DefaultDedupThreshold 实现 DedupThresholdAware：词面哈希对近重复文本约 0.6。
func (e *HashEmbedder) DefaultDedupThreshold() float64 { return 0.60 }

// ngramWeight 不同长度 n-gram 的特征权重：越长越具区分度。
func ngramWeight(n int) float32 {
	switch n {
	case 1:
		return 0.5
	case 2:
		return 1.0
	default:
		return 1.5
	}
}

// Embed 实现 Embedder：对 rune 1/2/3-gram 与整词做符号哈希后累加，L2 归一化。
func (e *HashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = e.embedOne(t)
	}
	return out, nil
}

func (e *HashEmbedder) embedOne(text string) []float32 {
	vec := make([]float32, e.dim)
	runes := []rune(strings.ToLower(text))

	addFeature := func(key string, w float32) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(key))
		bucket := int(h.Sum32() % uint32(e.dim))
		if h.Sum32()&(1<<31) != 0 { // 符号随机化，减少哈希碰撞的系统性偏置
			vec[bucket] -= w
		} else {
			vec[bucket] += w
		}
	}

	// rune n-gram（n=1,2,3），适配中英文混合文本
	for n := 1; n <= 3; n++ {
		for i := 0; i+n <= len(runes); i++ {
			gram := string(runes[i : i+n])
			if isAllSpace(gram) {
				continue
			}
			addFeature(fmt.Sprintf("g%d:%s", n, gram), ngramWeight(n))
		}
	}
	// 整词特征，强化英文词级匹配
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		addFeature("w:"+strings.ToLower(word), 2.0)
	}

	// L2 归一化，使余弦相似度只反映方向
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] /= float32(norm)
		}
	}
	return vec
}

func isAllSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
