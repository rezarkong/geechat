package file

import (
	"GopherAI/common/rag"
	"GopherAI/config"
	"GopherAI/utils"
	"context"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	RagIndexStatusNone     = "none"
	RagIndexStatusIndexing = "indexing"
	RagIndexStatusSuccess  = "success"
	RagIndexStatusFailed   = "failed"
)

type RagIndexStatus struct {
	Status    string `json:"index_status"`
	FilePath  string `json:"file_path,omitempty"`
	Message   string `json:"index_msg,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

var (
	ragIndexStatusMu sync.RWMutex
	ragIndexStatuses = map[string]RagIndexStatus{}
)

func GetRagIndexStatus(username string) RagIndexStatus {
	ragIndexStatusMu.RLock()
	defer ragIndexStatusMu.RUnlock()

	status, ok := ragIndexStatuses[username]
	if !ok {
		return RagIndexStatus{Status: RagIndexStatusNone}
	}
	return status
}

func setRagIndexStatus(username string, status RagIndexStatus) {
	status.UpdatedAt = time.Now().Unix()
	ragIndexStatusMu.Lock()
	defer ragIndexStatusMu.Unlock()
	ragIndexStatuses[username] = status
}

func finishRagIndexStatus(username, filePath, status, message string) {
	ragIndexStatusMu.Lock()
	defer ragIndexStatusMu.Unlock()

	current, ok := ragIndexStatuses[username]
	if ok && current.FilePath != filePath {
		return
	}
	ragIndexStatuses[username] = RagIndexStatus{
		Status:    status,
		FilePath:  filePath,
		Message:   message,
		UpdatedAt: time.Now().Unix(),
	}
}

func indexRagFileAsync(username, filename, filePath string) {
	ctx := context.Background()
	indexer, err := rag.NewRAGIndexer(ctx, filename, config.GetConfig().RagModelConfig.RagEmbeddingModel)
	if err != nil {
		log.Printf("Failed to create RAG indexer: %v", err)
		os.Remove(filePath)
		finishRagIndexStatus(username, filePath, RagIndexStatusFailed, err.Error())
		return
	}

	if err := indexer.IndexFile(ctx, filePath); err != nil {
		log.Printf("Failed to index file: %v", err)
		os.Remove(filePath)
		rag.DeleteIndex(ctx, filename)
		finishRagIndexStatus(username, filePath, RagIndexStatusFailed, err.Error())
		return
	}

	log.Printf("File indexed successfully: %s", filename)
	finishRagIndexStatus(username, filePath, RagIndexStatusSuccess, "")
}

// 上传rag相关文件（这里只允许文本文件）
// 其实可以直接将其向量化进行保存，但这边依旧存储到服务器上以便后续可以在服务器上查看历史RAG文件
func UploadRagFile(ctx context.Context, username string, file *multipart.FileHeader) (string, error) {
	// 校验文件类型和文件名
	if err := utils.ValidateFile(file); err != nil {
		log.Printf("File validation failed: %v", err)
		return "", err
	}

	// 创建用户目录
	userDir := filepath.Join("uploads", username)
	if err := os.MkdirAll(userDir, 0755); err != nil {
		log.Printf("Failed to create user directory %s: %v", userDir, err)
		return "", err
	}

	// 删除用户目录中的所有现有文件及其索引（每个用户只能有一个文件）
	files, err := os.ReadDir(userDir)
	if err == nil {
		for _, f := range files {
			if !f.IsDir() {
				filename := f.Name()
				// 删除该文件对应的 Redis 索引
				if err := rag.DeleteIndex(ctx, filename); err != nil {
					log.Printf("Failed to delete index for %s: %v", filename, err)
					// 继续执行，不因为索引删除失败而中断文件上传
				}
			}
		}
	}
	// 删除用户目录中的所有文件
	if err := utils.RemoveAllFilesInDir(userDir); err != nil {
		log.Printf("Failed to clean user directory %s: %v", userDir, err)
		return "", err
	}

	// 生成UUID作为唯一文件名
	uuid := utils.GenerateUUID()

	ext := filepath.Ext(file.Filename)
	filename := uuid + ext
	filePath := filepath.Join(userDir, filename)

	// 打开上传的文件
	src, err := file.Open()
	if err != nil {
		log.Printf("Failed to open uploaded file: %v", err)
		return "", err
	}
	defer src.Close()

	// 创建目标文件
	dst, err := os.Create(filePath)
	if err != nil {
		log.Printf("Failed to create destination file %s: %v", filePath, err)
		return "", err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		log.Printf("Failed to copy file content: %v", err)
		return "", err
	}

	log.Printf("File uploaded successfully: %s", filePath)

	setRagIndexStatus(username, RagIndexStatus{
		Status:   RagIndexStatusIndexing,
		FilePath: filePath,
	})
	go indexRagFileAsync(username, filename, filePath)

	return filePath, nil
}
