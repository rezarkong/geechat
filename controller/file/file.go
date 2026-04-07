package file

import (
	"GopherAI/common/code"
	"GopherAI/controller"
	"GopherAI/service/file"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
)

type (
	UploadFileResponse struct {
		FilePath string `json:"file_path,omitempty"`
		controller.Response
	}

	RagIndexStatusResponse struct {
		IndexStatus string `json:"index_status"`
		FilePath    string `json:"file_path,omitempty"`
		IndexMsg    string `json:"index_msg,omitempty"`
		UpdatedAt   int64  `json:"updated_at,omitempty"`
		controller.Response
	}
)

func UploadRagFile(c *gin.Context) {
	res := new(UploadFileResponse)
	uploadedFile, err := c.FormFile("file")
	if err != nil {
		log.Println("FormFile fail ", err)
		c.JSON(http.StatusBadRequest, res.CodeOf(code.CodeInvalidParams))
		return
	}

	username := c.GetString("userName")
	if username == "" {
		log.Println("Username not found in context")
		c.JSON(http.StatusUnauthorized, res.CodeOf(code.CodeInvalidToken))
		return
	}

	//indexer 会在 service 层根据实际文件名创建
	ctx := c.Request.Context()
	filePath, err := file.UploadRagFile(ctx, username, uploadedFile)
	if err != nil {
		log.Println("UploadFile fail ", err)
		c.JSON(http.StatusInternalServerError, res.CodeOf(code.CodeServerBusy))
		return
	}

	res.Success()
	res.FilePath = filePath
	c.JSON(http.StatusOK, res)
}

func GetRagIndexStatus(c *gin.Context) {
	res := new(RagIndexStatusResponse)

	username := c.GetString("userName")
	if username == "" {
		log.Println("Username not found in context")
		c.JSON(http.StatusUnauthorized, res.CodeOf(code.CodeInvalidToken))
		return
	}

	status := file.GetRagIndexStatus(username)
	res.Success()
	res.IndexStatus = status.Status
	res.FilePath = status.FilePath
	res.IndexMsg = status.Message
	res.UpdatedAt = status.UpdatedAt
	c.JSON(http.StatusOK, res)
}
