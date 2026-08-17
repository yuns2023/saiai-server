package service

import "github.com/gin-gonic/gin"

// writeAnthropicError writes the native Anthropic Messages error envelope.
func writeAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	if c == nil {
		return
	}
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}
