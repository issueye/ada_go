package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFakeOpenAIServer 构造一个 OpenAI /embeddings 兼容的假服务：
// 返回 4 维向量（按输入 index 生成可识别的特征值，并故意打乱 data 顺序）。
func newFakeOpenAIServer(t *testing.T, capture func(path, auth, model string, input []string)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析请求失败: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if capture != nil {
			capture(r.URL.Path, r.Header.Get("Authorization"), req.Model, req.Input)
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		items := make([]item, len(req.Input))
		// 故意倒序填充，验证客户端按 index 还原
		for i := range req.Input {
			idx := len(req.Input) - 1 - i
			items[i] = item{Index: idx, Embedding: []float32{float32(idx + 1), 0.5, 0.25, 0.125}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   items,
			"model":  req.Model,
			"usage":  map[string]int{"prompt_tokens": 1, "total_tokens": 1},
			"object": "list",
		})
	})
	return httptest.NewServer(mux)
}
