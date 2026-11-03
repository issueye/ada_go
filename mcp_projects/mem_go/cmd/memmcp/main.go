// memmcp 是基于 GoVector 的 MCP 长期记忆服务器。
//
// 以 stdio 传输运行，供 ZCode、Claude Desktop、Cursor 等 MCP 客户端接入。
// 数据零外部依赖：向量与元数据全部持久化在本地 BoltDB 文件中。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"mem_go/internal/mcpserver"
	"mem_go/internal/memory"
)

// version 是 memmcp 程序版本。
const version = "0.1.0"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[memmcp] ")

	var (
		dbPath  string
		showVer bool
	)
	flag.StringVar(&dbPath, "db", "", "记忆数据库文件路径（默认 $MEM_DB_PATH 或 ./mem_go.db）")
	flag.BoolVar(&showVer, "version", false, "打印版本号后退出")
	flag.Parse()
	if showVer {
		fmt.Println("memmcp", version)
		return
	}
	if dbPath == "" {
		dbPath = os.Getenv("MEM_DB_PATH")
	}
	if dbPath == "" {
		dbPath = "mem_go.db"
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	embedder, err := newEmbedderFromEnv(ctx)
	if err != nil {
		log.Fatalf("初始化嵌入模型失败: %v", err)
	}

	engine, err := memory.Open(memory.Config{
		DBPath:   dbPath,
		Embedder: embedder,
	})
	if err != nil {
		log.Fatalf("打开记忆库失败: %v", err)
	}
	defer engine.Close()
	log.Printf("记忆库已就绪: db=%s embedder=%s dim=%d", dbPath, embedder.Name(), embedder.Dim())

	server := mcpserver.NewServer(engine)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Printf("服务器退出: %v", err)
	}
}

// newEmbedderFromEnv 根据环境变量选择嵌入提供者：
//
//	MEM_EMBEDDING_PROVIDER = openai（默认，配置了 API Key 时）| hash（离线回退）
//	MEM_EMBEDDING_BASE_URL 默认 https://api.openai.com/v1
//	MEM_EMBEDDING_API_KEY  （或 OPENAI_API_KEY）
//	MEM_EMBEDDING_MODEL    默认 text-embedding-3-small
//	MEM_HASH_DIM           hash 提供者维度，默认 512
func newEmbedderFromEnv(ctx context.Context) (memory.Embedder, error) {
	provider := os.Getenv("MEM_EMBEDDING_PROVIDER")
	if provider == "" {
		if hasAPIKey() {
			provider = "openai"
		} else {
			provider = "hash"
		}
	}

	switch provider {
	case "openai":
		apiKey := apiKey()
		if apiKey == "" {
			return nil, fmt.Errorf("未配置 API Key：请设置 MEM_EMBEDDING_API_KEY（或 OPENAI_API_KEY），或将 MEM_EMBEDDING_PROVIDER 设为 hash 以离线运行")
		}
		return memory.NewOpenAIEmbedder(ctx,
			os.Getenv("MEM_EMBEDDING_BASE_URL"),
			apiKey,
			os.Getenv("MEM_EMBEDDING_MODEL"))
	case "hash":
		dim := memory.DefaultHashDim
		if v := os.Getenv("MEM_HASH_DIM"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				dim = n
			}
		}
		return memory.NewHashEmbedder(dim), nil
	default:
		return nil, fmt.Errorf("未知的 MEM_EMBEDDING_PROVIDER %q（可选: openai, hash）", provider)
	}
}

func hasAPIKey() bool { return apiKey() != "" }

func apiKey() string {
	if k := os.Getenv("MEM_EMBEDDING_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("OPENAI_API_KEY")
}
