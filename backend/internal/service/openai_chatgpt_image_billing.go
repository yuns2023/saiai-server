package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ChatGPTImageBilling is frozen at the first successful terminal delivery.
// Subsequent retries may disclose more metadata, but cannot reprice a debit.
type ChatGPTImageBilling struct {
	Count   int
	Size    string
	CostUSD float64
}

func ValidChatGPTImagePrices(prices map[string]float64) bool {
	if prices == nil {
		return true
	}
	if len(prices) != 4 {
		return false
	}
	for _, size := range []string{"1K", "2K", "4K", "unknown"} {
		price, ok := prices[size]
		if !ok || !validChatGPTConfiguredPrice(price) {
			return false
		}
	}
	return true
}

func CloneChatGPTImagePrices(prices map[string]float64) map[string]float64 {
	if prices == nil {
		return nil
	}
	copy := make(map[string]float64, len(prices))
	for size, price := range prices {
		copy[size] = price
	}
	return copy
}

func ChatGPTImageDimensions(width, height int) string {
	if width <= 0 || height <= 0 || width > 32768 || height > 32768 {
		return ""
	}
	return fmt.Sprintf("%dx%d", width, height)
}

func normalizeChatGPTImageSize(size string) string {
	if size == "conflict" {
		return size
	}
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return ""
	}
	width, wErr := strconv.Atoi(parts[0])
	height, hErr := strconv.Atoi(parts[1])
	if wErr != nil || hErr != nil {
		return ""
	}
	return ChatGPTImageDimensions(width, height)
}

// Resolution brackets use the observed longer edge. No model name, requested
// size, thinking effort or default UI label is evidence of output dimensions.
func ChatGPTImageSizeClass(size string) string {
	size = normalizeChatGPTImageSize(size)
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return "unknown"
	}
	width, _ := strconv.Atoi(parts[0])
	height, _ := strconv.Atoi(parts[1])
	edge := max(width, height)
	switch {
	case edge <= 1024:
		return "1K"
	case edge <= 2048:
		return "2K"
	case edge <= 4096:
		return "4K"
	default:
		return "unknown"
	}
}

func CalculateChatGPTImageBilling(images ChatGPTImageEvidence, prices map[string]float64) (*ChatGPTImageBilling, error) {
	if !ValidChatGPTImagePrices(prices) {
		return nil, errors.New("invalid native Chat image prices")
	}
	images = MergeChatGPTImageEvidence(ChatGPTImageEvidence{}, images)
	result := &ChatGPTImageBilling{Count: len(images.AssetHashes)}
	for i := range images.AssetHashes {
		size := images.AssetSizes[i]
		if size == "conflict" {
			size = ""
		}
		if i == 0 {
			result.Size = size
		} else if result.Size != size {
			result.Size = "mixed"
		}
		result.CostUSD += prices[ChatGPTImageSizeClass(size)]
	}
	if !validChatGPTConfiguredPrice(result.CostUSD) {
		return nil, errors.New("invalid native Chat image total")
	}
	return result, nil
}
