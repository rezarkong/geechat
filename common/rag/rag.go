package rag

import (
	"GopherAI/common/redis"
	redisPkg "GopherAI/common/redis"
	"GopherAI/config"
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	embeddingArk "github.com/cloudwego/eino-ext/components/embedding/ark"
	redisIndexer "github.com/cloudwego/eino-ext/components/indexer/redis"
	redisRetriever "github.com/cloudwego/eino-ext/components/retriever/redis"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	redisCli "github.com/redis/go-redis/v9"
)

type RAGIndexer struct {
	embedding embedding.Embedder
	indexer   *redisIndexer.Indexer
}

type RAGQuery struct {
	embedding embedding.Embedder
	retriever retriever.Retriever
}

const (
	ragChunkBytes            = 3000
	ragChunkOverlap          = 300
	ragChunkOverlapSentences = 1
	ragEmbedMaxBytes         = 8192
	ragEmbedBatchSize        = 2
)

var ragEmbedderCache sync.Map

type ragEmbedderCacheKey struct {
	baseURL string
	apiKey  string
	model   string
}

func getRAGEmbedder(ctx context.Context, baseURL, apiKey, model string) (*embeddingArk.Embedder, error) {
	key := ragEmbedderCacheKey{
		baseURL: baseURL,
		apiKey:  apiKey,
		model:   model,
	}
	if cached, ok := ragEmbedderCache.Load(key); ok {
		return cached.(*embeddingArk.Embedder), nil
	}

	embedder, err := embeddingArk.NewEmbedder(ctx, &embeddingArk.EmbeddingConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
	})
	if err != nil {
		return nil, err
	}

	actual, _ := ragEmbedderCache.LoadOrStore(key, embedder)
	return actual.(*embeddingArk.Embedder), nil
}

// 语句分割
func splitSentence(text string) []string {
	isEnd := func(r rune) bool {
		switch r {
		case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
			return true
		default:
			return false
		}
	}
	sentences := make([]string, 0)
	var buf strings.Builder
	for _, r := range text {
		buf.WriteRune(r)
		if isEnd(r) {
			s := strings.TrimSpace(buf.String())
			if s != "" {
				sentences = append(sentences, s)
			}
			buf.Reset()
		}
	}
	if tail := strings.TrimSpace(buf.String()); tail != "" {
		sentences = append(sentences, tail)
	}
	return sentences
}

// 万一是一个没有标点符号的超长文本
func fallbackByteSplit(text string, chunkSize int) []string {
	var chunks []string
	for start := 0; start < len(text); {
		end := start + chunkSize
		if end > len(text) {
			end = len(text)
		} else {
			for end > start && !utf8.ValidString(text[start:end]) {
				end--
			}
		}
		chunks = append(chunks, text[start:end])
		start = end
	}
	return chunks
}
func splitText(text string, chunkSize, overlap int) []string {
	if chunkSize <= 0 || text == "" {
		return nil
	}
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= chunkSize {
		overlap = chunkSize / 5
	}

	sentences := splitSentence(text)
	if len(sentences) == 0 {
		return nil
	} else if len(sentences) == 1 {

	}
	chunks := make([]string, 0)
	for start := 0; start < len(sentences); {
		var current strings.Builder
		end := start
		// 写入足量的 sentences
		for end < len(sentences) {
			next := sentences[end]
			if current.Len() > 0 && current.Len()+len(next) > chunkSize {
				break
			}
			current.WriteString(next)
			end++
		}
		if current.Len() == 0 {
			// 单句超过 chunkSize，兜底切分，避免死循环
			longSentence := sentences[start]
			if len(longSentence) > chunkSize {
				chunks = append(chunks, fallbackByteSplit(longSentence, chunkSize)...)
			} else {
				chunks = append(chunks, longSentence)
			}
			start++
			continue
		}
		chunks = append(chunks, current.String())

		if end >= len(sentences) {
			break
		}

		nextStart := end - ragChunkOverlapSentences
		if nextStart <= start {
			nextStart = end
		}
		start = nextStart
	}
	return chunks
}

// 构建知识库索引
// 专业说法：文本解析、文本切块、向量化、存储向量
// 通俗理解：把“人能读的文档”，转换成“AI 能按语义搜索的格式”，并存起来
func NewRAGIndexer(ctx context.Context, filename, embeddingModel string) (*RAGIndexer, error) {

	// 从环境变量中读取调用向量模型所需的 API Key
	apiKey := os.Getenv("OPENAI_API_KEY")

	// 向量的维度大小（等于向量模型输出的数字个数）
	// Redis 在创建向量索引时必须提前知道这个值
	dimension := config.GetConfig().RagModelConfig.RagDimension

	// 创建向量生成器实例，并按 baseURL/apiKey/model 复用
	// 后续所有文本的“向量化”都会通过它完成
	embedder, err := getRAGEmbedder(ctx, config.GetConfig().RagModelConfig.RagBaseUrl, apiKey, embeddingModel)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	// ===============================
	// 2. 初始化 Redis 中的向量索引结构
	// ===============================
	// 可以理解为：先在 Redis 里建好“仓库”，
	// 告诉它以后要存向量，并且每个向量的维度是多少
	if err := redisPkg.InitRedisIndex(ctx, filename, dimension); err != nil {
		return nil, fmt.Errorf("failed to init redis index: %w", err)
	}

	// 获取 Redis 客户端，用于后续数据写入
	rdb := redisPkg.Rdb

	// ===============================
	// 3. 配置索引器（定义：文档如何被存进 Redis）
	// ===============================
	indexerConfig := &redisIndexer.IndexerConfig{
		Client:    rdb,                                     // Redis 客户端
		KeyPrefix: redis.GenerateIndexNamePrefix(filename), // 不同知识库使用不同前缀，避免冲突
		BatchSize: ragEmbedBatchSize,                       // 每批最多 2 个 chunk，避免超过 Ark 的单次输入长度限制

		// 定义：一段文档（Document）在 Redis 中该如何存储
		DocumentToHashes: func(ctx context.Context, doc *schema.Document) (*redisIndexer.Hashes, error) {

			// 从文档的元数据中取出来源信息（例如文件名、URL）
			source := ""
			if s, ok := doc.MetaData["source"].(string); ok {
				source = s
			}
			chunkIndex := 0
			if v, ok := doc.MetaData["chunk_index"]; ok {
				switch vv := v.(type) {
				case int:
					chunkIndex = vv
				case int32:
					chunkIndex = int(vv)
				case int64:
					chunkIndex = int(vv)
				case float64:
					chunkIndex = int(vv)
				case string:
					parsed, err := strconv.Atoi(vv)
					if err == nil {
						chunkIndex = parsed
					}
				}
			}

			// 构造 Redis 中实际存储的数据结构（Hash）
			return &redisIndexer.Hashes{
				// Redis Key，一般由“知识库名 + 文档块 ID”组成
				Key: fmt.Sprintf("%s:%s", filename, doc.ID),

				// Redis Hash 中的字段
				Field2Value: map[string]redisIndexer.FieldValue{
					// content：原始文本内容
					// EmbedKey 表示：该字段需要先做向量化，
					// 生成的向量会存入名为 "vector" 的字段中
					"content": {Value: doc.Content, EmbedKey: "vector"},

					// source / chunk_index：辅助定位原始文档块，不参与向量计算
					"source":      {Value: source},
					"chunk_index": {Value: chunkIndex},
				},
			}, nil
		},
	}
	// 将“向量生成器”交给索引器
	// 这样索引器在写入文本时，可以自动完成向量计算
	indexerConfig.Embedding = embedder

	// ===============================
	// 4. 创建最终可用的索引器实例
	// ===============================
	// 此时索引器已经具备：
	// - 文本 → 向量 的能力
	// - 向量写入 Redis 的能力
	idx, err := redisIndexer.NewIndexer(ctx, indexerConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create indexer: %w", err)
	}

	// 返回一个封装好的 RAGIndexer，
	// 后续只需要调用它，就可以把文档加入知识库
	return &RAGIndexer{
		embedding: embedder,
		indexer:   idx,
	}, nil
}

// IndexFile 读取文件内容并创建向量索引
func (r *RAGIndexer) IndexFile(ctx context.Context, filePath string) error {
	// 读取文件内容
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// 将文件内容转换为文档
	chunks := splitText(string(content), ragChunkBytes, ragChunkOverlap)
	if len(chunks) == 0 {
		return fmt.Errorf("empty file content")
	}
	docs := make([]*schema.Document, 0, len(chunks))
	for i, chunk := range chunks {
		if len(chunk) == 0 || len(chunk) > ragEmbedMaxBytes {
			return fmt.Errorf("invalid chunk size: chunk=%d bytes=%d", i+1, len(chunk))
		}
		docs = append(docs, &schema.Document{
			ID:      fmt.Sprintf("doc_%d", i+1),
			Content: chunk,
			MetaData: map[string]any{
				"source":      filePath,
				"chunk_index": i + 1,
			},
		})
	}
	log.Printf("RAG split file=%s size=%d bytes chunks=%d first_chunk_bytes=%d", filePath, len(content), len(docs), len(docs[0].Content))
	// 使用 indexer 存储文档（会自动进行向量化）
	_, err = r.indexer.Store(ctx, docs)
	if err != nil {
		return fmt.Errorf("failed to store document: %w", err)
	}
	//prefix := redis.GenerateIndexNamePrefix(filepath.Base(filePath))
	//iter := redisPkg.Rdb.Scan(ctx, 0, prefix+"*", 10).Iterator()
	//for iter.Next(ctx) {
	//	key := iter.Val()
	//	vec, err := redisPkg.GetHashVector(ctx, key)
	//	if err != nil {
	//		log.Printf("read vector failed, key=%s err=%v", key, err)
	//		continue
	//	}
	//	show := 8
	//	if len(vec) < show {
	//		show = len(vec)
	//	}
	//	log.Printf("RAG vector key=%s dim=%d first=%v", key, len(vec), vec[:show])
	//	break
	//}
	//if err := iter.Err(); err != nil {
	//	log.Printf("scan vector keys failed: %v", err)
	//}
	return nil
}

// DeleteIndex 删除指定文件的知识库索引（静态方法，不依赖实例）
func DeleteIndex(ctx context.Context, filename string) error {
	if err := redisPkg.DeleteRedisIndex(ctx, filename); err != nil {
		return fmt.Errorf("failed to delete redis index: %w", err)
	}
	return nil
}

// NewRAGQuery 创建 RAG 查询器（用于向量检索和问答）
func NewRAGQuery(ctx context.Context, username string) (*RAGQuery, error) {
	cfg := config.GetConfig()
	apiKey := os.Getenv("OPENAI_API_KEY")

	// 创建 embedding 模型，并按 baseURL/apiKey/model 复用
	embedder, err := getRAGEmbedder(ctx, cfg.RagModelConfig.RagBaseUrl, apiKey, cfg.RagModelConfig.RagEmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	// 获取用户上传的文件名（假设每个用户只有一个文件）
	// 这里需要从用户目录读取文件名
	userDir := fmt.Sprintf("uploads/%s", username)
	files, err := os.ReadDir(userDir)
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no uploaded file found for user %s", username)
	}

	var filename string
	for _, f := range files {
		if !f.IsDir() {
			filename = f.Name()
			break
		}
	}

	if filename == "" {
		return nil, fmt.Errorf("no valid file found for user %s", username)
	}

	// 创建 retriever
	rdb := redisPkg.Rdb
	indexName := redis.GenerateIndexName(filename)

	retrieverConfig := &redisRetriever.RetrieverConfig{
		Client:       rdb,
		Index:        indexName,
		Dialect:      2,
		ReturnFields: []string{"content", "source", "chunk_index", "distance"},
		TopK:         5,
		VectorField:  "vector",
		DocumentConverter: func(ctx context.Context, doc redisCli.Document) (*schema.Document, error) {
			resp := &schema.Document{
				ID:       doc.ID,
				Content:  "",
				MetaData: map[string]any{},
			}
			for field, val := range doc.Fields {
				if field == "content" {
					resp.Content = val
					continue
				}
				if field == "chunk_index" {
					parsed, err := strconv.Atoi(val)
					if err == nil {
						resp.MetaData[field] = parsed
						continue
					}
				}
				resp.MetaData[field] = val
			}
			return resp, nil
		},
	}
	retrieverConfig.Embedding = embedder
	rtr, err := redisRetriever.NewRetriever(ctx, retrieverConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create retriever: %w", err)
	}

	return &RAGQuery{
		embedding: embedder,
		retriever: rtr,
	}, nil
}

// RetrieveDocuments 检索相关文档
func (r *RAGQuery) RetrieveDocuments(ctx context.Context, query string) ([]*schema.Document, error) {
	docs, err := r.retriever.Retrieve(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve documents: %w", err)
	}
	return docs, nil
}

// BuildRAGPrompt 构建包含检索文档的提示词
func BuildRAGPrompt(query string, docs []*schema.Document) string {
	if len(docs) == 0 {
		return query
	}

	contextText := ""
	for i, doc := range docs {
		source, _ := doc.MetaData["source"].(string)
		chunkIndex, ok := doc.MetaData["chunk_index"]
		if source != "" || ok {
			contextText += fmt.Sprintf("[文档 %d][source=%s][chunk=%v]: %s\n\n", i+1, source, chunkIndex, doc.Content)
			continue
		}
		contextText += fmt.Sprintf("[文档 %d]: %s\n\n", i+1, doc.Content)
	}

	prompt := fmt.Sprintf(`基于以下参考文档回答用户的问题。如果文档中没有相关信息，请说明无法找到相关信息。

参考文档：
%s

用户问题：%s

请提供准确、完整的回答：`, contextText, query)

	return prompt
}
