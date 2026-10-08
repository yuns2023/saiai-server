package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// This limit keeps native accounting snapshots below their existing 8 KiB
// bound. Counts are observed generated assets, not a provider image invoice.
const MaxChatGPTObservedImages = 64

type ChatGPTImageEvidence struct {
	GenerationSeen bool
	AssetHashes    []string
	// Sizes align with hashes; empty means unknown. Only observed dimensions
	// are retained, never pointers, prompts or image bytes.
	AssetSizes []string
}

// MergeChatGPTImageEvidence unions content-hiding identities across delivery
// retries. Invalid digests and excess assets are ignored, never persisted.
func MergeChatGPTImageEvidence(first, second ChatGPTImageEvidence) ChatGPTImageEvidence {
	out := ChatGPTImageEvidence{GenerationSeen: first.GenerationSeen || second.GenerationSeen}
	seen := make(map[string]string)
	for _, evidence := range []ChatGPTImageEvidence{first, second} {
		for i, hash := range evidence.AssetHashes {
			if len(hash) != sha256.Size*2 {
				continue
			}
			if _, err := hex.DecodeString(hash); err != nil {
				continue
			}
			hash = strings.ToLower(hash)
			if len(seen) >= MaxChatGPTObservedImages {
				if _, exists := seen[hash]; !exists {
					continue
				}
			}
			size := ""
			if i < len(evidence.AssetSizes) {
				size = normalizeChatGPTImageSize(evidence.AssetSizes[i])
			}
			prior, exists := seen[hash]
			if exists && prior != "" && size != "" && prior != size {
				seen[hash] = "conflict"
			} else if !exists || prior == "" {
				seen[hash] = size
			}
		}
	}
	for hash := range seen {
		out.AssetHashes = append(out.AssetHashes, hash)
	}
	if len(out.AssetHashes) > 0 {
		out.GenerationSeen = true
	}
	sort.Strings(out.AssetHashes)
	for _, hash := range out.AssetHashes {
		out.AssetSizes = append(out.AssetSizes, seen[hash])
	}
	return out
}

type chatGPTImageObserver struct {
	seen            bool
	currentTool     bool
	currentComplete bool
	currentID       string            // digest of the current message identity
	assets          map[string]string // asset digest -> message digest
	sizes           map[string]string // asset digest -> observed dimensions
}

func chatGPTImageDigest(value string) string {
	digest := sha256.Sum256([]byte("chatgpt-generated-image-v1\x00" + value))
	return hex.EncodeToString(digest[:])
}

func (o *chatGPTImageObserver) observeEvent(event map[string]json.RawMessage) {
	o.observeDelta(event, 0)
}

func (o *chatGPTImageObserver) observeDelta(event map[string]json.RawMessage, depth int) {
	if depth > 8 {
		return
	}
	if message := event["message"]; len(message) > 0 {
		o.observeMessage(message)
		return
	}
	var nested map[string]json.RawMessage
	if json.Unmarshal(event["v"], &nested) == nil && len(nested["message"]) > 0 {
		o.observeMessage(nested["message"])
		return
	}
	// Inspect only explicit image-part patches for an already identified image
	// tool message. Never scan arbitrary user text or apply opaque text deltas.
	var path, op string
	_ = json.Unmarshal(event["p"], &path)
	_ = json.Unmarshal(event["o"], &op)
	if path == "/message" && (op == "add" || op == "replace") {
		o.observeMessage(event["v"])
		return
	}
	if op == "patch" && path == "" {
		var patches []map[string]json.RawMessage
		if json.Unmarshal(event["v"], &patches) == nil {
			for i, patch := range patches {
				if i >= 256 {
					break
				}
				o.observeDelta(patch, depth+1)
			}
		}
		return
	}
	if !o.currentTool || (op != "append" && op != "add" && op != "replace") {
		return
	}
	if path == "/message/content/parts" || strings.HasPrefix(path, "/message/content/parts/") {
		var parts []json.RawMessage
		if json.Unmarshal(event["v"], &parts) == nil {
			for _, part := range parts {
				o.observePart(part, false)
			}
		} else {
			o.observePart(event["v"], false)
		}
	}
}

func chatGPTImageTool(name string) bool {
	switch name {
	case "image_gen", "image_gen.text2im", "t2uay3k", "t2uay3k.sj1i4kz", "dalle", "dalle.text2im":
		return true
	}
	return false
}

func (o *chatGPTImageObserver) observeMessage(raw json.RawMessage) {
	var message struct {
		ID      string                      `json:"id"`
		Author  struct{ Role, Name string } `json:"author"`
		Status  string                      `json:"status"`
		Content struct {
			Type  string            `json:"content_type"`
			Parts []json.RawMessage `json:"parts"`
			Text  string            `json:"text"`
		} `json:"content"`
		Metadata struct {
			IsError bool            `json:"is_error"`
			Async   json.RawMessage `json:"image_gen_async"`
			Paragen json.RawMessage `json:"image_gen_paragen_metadata"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return
	}
	o.currentID = chatGPTImageDigest(message.ID)
	o.currentTool = message.Author.Role == "tool" && chatGPTImageTool(message.Author.Name)
	o.currentComplete = message.Status == "finished_successfully"
	if message.Author.Role != "assistant" && !o.currentTool {
		return
	}
	if o.currentTool {
		o.seen = true
	}
	if message.Metadata.IsError || message.Status == "finished_failed" ||
		message.Content.Type == "error" || message.Content.Type == "system_error" {
		o.currentTool = false
		o.currentComplete = false
		if message.ID != "" {
			for asset, owner := range o.assets {
				if owner == o.currentID {
					delete(o.assets, asset)
					delete(o.sizes, asset)
				}
			}
		}
		return
	}
	if message.Content.Type == "multimodal_text" {
		inProgress := chatGPTImagePending(message.Metadata.Async) || chatGPTImagePending(message.Metadata.Paragen)
		for _, part := range message.Content.Parts {
			o.observePart(part, inProgress)
		}
	}
	// Desktop also renders structured Responses-style image_generation_call
	// items serialized in assistant/tool text. Only this explicit item schema
	// is accepted; ordinary prose containing image URLs is never counted.
	if message.Content.Type == "text" {
		if message.Content.Text != "" {
			o.observeCalls(message.Content.Text)
		}
		for _, part := range message.Content.Parts {
			var text string
			if json.Unmarshal(part, &text) == nil {
				o.observeCalls(text)
			}
		}
	}
}

func chatGPTImagePending(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "false"
}

func (o *chatGPTImageObserver) addAsset(asset, size string) {
	if asset == "" {
		return
	}
	o.seen = true
	digest := chatGPTImageDigest(asset)
	if o.assets == nil {
		o.assets = make(map[string]string)
		o.sizes = make(map[string]string)
	}
	_, exists := o.assets[digest]
	if exists || len(o.assets) < MaxChatGPTObservedImages {
		o.assets[digest] = o.currentID
		prior := o.sizes[digest]
		if prior != "" && size != "" && prior != size {
			o.sizes[digest] = "conflict"
		} else if prior == "" {
			o.sizes[digest] = size
		}
	}
}

func (o *chatGPTImageObserver) observePart(raw json.RawMessage, inProgress bool) {
	var envelope struct {
		Type   string          `json:"content_type"`
		Image  json.RawMessage `json:"image_asset_pointer"`
		Width  int             `json:"width"`
		Height int             `json:"height"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "image_asset_pointer" {
		return
	}
	if len(envelope.Image) > 0 {
		raw = envelope.Image
	}
	var part struct {
		Type     string `json:"content_type"`
		Asset    string `json:"asset_pointer"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		Metadata struct {
			Generation *struct {
				Height int `json:"height"`
			} `json:"generation"`
			Dalle json.RawMessage `json:"dalle"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &part) != nil {
		return
	}
	generated := o.currentTool || part.Metadata.Generation != nil || len(part.Metadata.Dalle) > 0 && string(part.Metadata.Dalle) != "null"
	if !generated {
		return
	}
	o.seen = true
	complete := o.currentComplete
	if part.Metadata.Generation != nil && part.Height > 0 {
		inProgress = part.Metadata.Generation.Height < part.Height
		complete = !inProgress
	}
	if complete && !inProgress && (strings.HasPrefix(part.Asset, "sediment://") || strings.HasPrefix(part.Asset, "file-service://")) {
		size := ChatGPTImageDimensions(part.Width, part.Height)
		outer := ChatGPTImageDimensions(envelope.Width, envelope.Height)
		if size == "" {
			size = outer
		} else if outer != "" && outer != size {
			size = "conflict"
		}
		o.addAsset(part.Asset, size)
	}
}

func (o *chatGPTImageObserver) observeCalls(text string) {
	var calls []struct{ Type, Status, Result, Size string }
	if json.Unmarshal([]byte(text), &calls) != nil {
		return
	}
	for _, call := range calls {
		if call.Type != "image_generation_call" {
			continue
		}
		o.seen = true
		if call.Status != "completed" {
			continue
		}
		if strings.HasPrefix(call.Result, "sediment://") || strings.HasPrefix(call.Result, "file-service://") ||
			strings.HasPrefix(call.Result, "data:image/") || strings.HasPrefix(call.Result, "iVBORw0KGgo") {
			size := chatGPTImageResultDimensions(call.Result)
			if size == "" {
				size = normalizeChatGPTImageSize(call.Size)
			}
			o.addAsset(call.Result, size)
		}
	}
}
