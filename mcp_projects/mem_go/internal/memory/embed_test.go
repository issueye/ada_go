package memory

import (
	"context"
	"math"
	"testing"
)

// cosine 计算两个向量的余弦相似度（测试辅助）。
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func TestHashEmbedderDeterministic(t *testing.T) {
	e := NewHashEmbedder(128)
	ctx := context.Background()

	a1, err := e.Embed(ctx, []string{"用户喜欢 Go 语言"})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := e.Embed(ctx, []string{"用户喜欢 Go 语言"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a1[0]) != 128 || len(a2[0]) != 128 {
		t.Fatalf("维度应为 128: %d, %d", len(a1[0]), len(a2[0]))
	}
	for i := range a1[0] {
		if a1[0][i] != a2[0][i] {
			t.Fatalf("相同文本的嵌入必须确定性: index %d 不一致", i)
		}
	}
}

func TestHashEmbedderNormalized(t *testing.T) {
	e := NewHashEmbedder(64)
	vecs, err := e.Embed(context.Background(), []string{"hello world"})
	if err != nil {
		t.Fatal(err)
	}
	var norm float64
	for _, v := range vecs[0] {
		norm += float64(v) * float64(v)
	}
	if d := math.Abs(math.Sqrt(norm) - 1); d > 1e-6 {
		t.Fatalf("向量应 L2 归一化，模长 %f", math.Sqrt(norm))
	}
}

func TestHashEmbedderSimilarityOrdering(t *testing.T) {
	e := NewHashEmbedder(128)
	ctx := context.Background()

	q, _ := e.Embed(ctx, []string{"用户偏好在终端使用 Vim 编辑代码"})
	similar, _ := e.Embed(ctx, []string{"用户在终端里使用 Vim 编辑"})
	diffent, _ := e.Embed(ctx, []string{"周报需要在每周五提交给经理"})

	if cosine(q[0], similar[0]) <= cosine(q[0], diffent[0]) {
		t.Fatalf("相近文本相似度应更高: %f vs %f",
			cosine(q[0], similar[0]), cosine(q[0], diffent[0]))
	}
}

func TestHashEmbedderDimFloor(t *testing.T) {
	e := NewHashEmbedder(1) // 低于下限应被钳制到 64
	if e.Dim() != 64 {
		t.Fatalf("维度下限应钳制到 64，得到 %d", e.Dim())
	}
}

func TestOpenAIEmbedderRequestShape(t *testing.T) {
	// 用本地 httptest 验证请求与解析逻辑
	var gotPath, gotAuth, gotModel string
	var gotInput []string
	srv := newFakeOpenAIServer(t, func(path, auth, model string, input []string) {
		gotPath, gotAuth, gotModel, gotInput = path, auth, model, input
	})
	defer srv.Close()

	e, err := NewOpenAIEmbedder(context.Background(), srv.URL, "test-key", "test-model")
	if err != nil {
		t.Fatalf("创建嵌入器失败: %v", err)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("应请求 /embeddings，得到 %s", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization 头错误: %s", gotAuth)
	}
	if gotModel != "test-model" {
		t.Fatalf("模型名错误: %s", gotModel)
	}
	if len(gotInput) == 0 || gotInput[0] != "dim probe" {
		t.Fatalf("探测输入错误: %v", gotInput)
	}
	if e.Dim() != 4 {
		t.Fatalf("探测维度应为 4，得到 %d", e.Dim())
	}

	// 批量顺序还原（fake 服务会打乱 data 顺序）
	vecs, err := e.Embed(context.Background(), []string{"一", "二", "三"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || vecs[0][0] != 1 || vecs[1][0] != 2 || vecs[2][0] != 3 {
		t.Fatalf("批量嵌入顺序还原失败: %v", vecs)
	}
}
